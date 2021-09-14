package common

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/apex/log"
	"github.com/shalb/cluster.dev/pkg/config"
	"github.com/shalb/cluster.dev/pkg/executor"
	"github.com/shalb/cluster.dev/pkg/project"
)

// moduleTypeKeyTf - string representation of this module type.
const moduleTypeKeyTf = "terraform"
const moduleTypeKeyKubernetes = "kubernetes"
const moduleTypeKeyHelm = "helm"
const remoteStateMarkerName = "RemoteStateMarkers"
const insertYAMLMarkerName = "insertYAMLMarkers"

var terraformBin = "terraform"

type hookSpec struct {
	Command   string `json:"command"`
	OnDestroy bool   `yaml:"on_destroy,omitempty" json:"on_destroy,omitempty"`
	OnApply   bool   `yaml:"on_apply,omitempty" json:"on_apply,omitempty"`
	OnPlan    bool   `yaml:"on_plan,omitempty" json:"on_plan,omitempty"`
}

type RequiredProvider struct {
	Source  string `json:"source"`
	Version string `json:"version"`
}

// Module describe cluster.dev module to deploy/destroy terraform modules.
type Module struct {
	infraPtr          *project.Infrastructure
	projectPtr        *project.Project
	backendPtr        project.Backend
	name              string
	dependencies      []*project.Dependency
	expectedOutputs   map[string]bool
	preHook           *hookSpec
	postHook          *hookSpec
	codeDir           string
	filesList         map[string][]byte
	providers         interface{}
	specRaw           map[string]interface{}
	markers           map[string]interface{}
	applyOutput       []byte
	requiredProviders map[string]RequiredProvider
}

func (m *Module) AddRequiredProvider(name, source, version string) {
	if m.requiredProviders == nil {
		m.requiredProviders = make(map[string]RequiredProvider)
	}
	m.requiredProviders[name] = RequiredProvider{
		Version: version,
		Source:  source,
	}
}

func (m *Module) Markers() map[string]interface{} {
	return m.markers
}

func (m *Module) FilesList() map[string][]byte {
	return m.filesList
}

func (m *Module) ReadConfigCommon(spec map[string]interface{}, infra *project.Infrastructure) error {
	// Check if CDEV_TF_BINARY is set to change terraform binary name.
	envTfBin, exists := os.LookupEnv("CDEV_TF_BINARY")
	if exists {
		terraformBin = envTfBin
	}
	mName, ok := spec["name"]
	if !ok {
		return fmt.Errorf("Incorrect module name")
	}

	m.infraPtr = infra
	m.projectPtr = infra.ProjectPtr
	m.name = mName.(string)
	m.expectedOutputs = make(map[string]bool)
	m.filesList = make(map[string][]byte)
	m.specRaw = spec
	m.markers = make(map[string]interface{})

	// Process dependencies.
	var modDeps []*project.Dependency
	var err error
	dependsOn, ok := spec["depends_on"]
	if ok {
		modDeps, err = m.readDeps(dependsOn)
		if err != nil {
			log.Debug(err.Error())
			return err
		}
	}
	m.dependencies = modDeps

	// Check and set backend.
	bPtr, exists := infra.ProjectPtr.Backends[infra.BackendName]
	if !exists {
		return fmt.Errorf("Backend '%s' not found, infra: '%s'", infra.BackendName, infra.Name)
	}
	m.backendPtr = bPtr

	// Process hooks.
	modPreHook, ok := spec["pre_hook"]
	if ok {
		m.preHook, err = readHook(modPreHook, "pre_hook")
		if err != nil {
			log.Debug(err.Error())
			return err
		}
	}
	modPostHook, ok := spec["post_hook"]
	if ok {
		m.postHook, err = readHook(modPostHook, "post_hook")
		if err != nil {
			log.Debug(err.Error())
			return err
		}
	}
	// Set providers.
	providers, exists := spec["providers"]
	if exists {
		m.providers = providers
	}
	m.codeDir = filepath.Join(m.ProjectPtr().CodeCacheDir, m.Key())
	return nil
}

func (m *Module) ExpectedOutputs() map[string]bool {
	return m.expectedOutputs
}

// Name return module name.
func (m *Module) Name() string {
	return m.name
}

// InfraPtr return ptr to module infrastructure.
func (m *Module) InfraPtr() *project.Infrastructure {
	return m.infraPtr
}

// ApplyOutput return output of last module applying.
func (m *Module) ApplyOutput() []byte {
	return m.applyOutput
}

// ProjectPtr return ptr to module project.
func (m *Module) ProjectPtr() *project.Project {
	return m.projectPtr
}

// InfraName return module infrastructure name.
func (m *Module) InfraName() string {
	return m.infraPtr.Name
}

// Backend return module backend.
func (m *Module) Backend() project.Backend {
	return m.infraPtr.Backend
}

// Dependencies return slice of module dependencies.
func (m *Module) Dependencies() *[]*project.Dependency {
	return &m.dependencies
}

func (m *Module) InitCommon() error {
	rn, err := executor.NewExecutor(m.codeDir)
	if err != nil {
		log.Debug(err.Error())
		return err
	}
	rn.Env = append(rn.Env, fmt.Sprintf("TF_PLUGIN_CACHE_DIR=%v", config.Global.PluginsCacheDir))
	rn.LogLabels = []string{
		m.InfraName(),
		m.Name(),
		"init",
	}

	var cmd = ""
	cmd += fmt.Sprintf("%[1]s init", terraformBin)
	var errMsg []byte
	m.projectPtr.InitLock.Lock()
	defer m.projectPtr.InitLock.Unlock()
	m.applyOutput, errMsg, err = rn.Run(cmd)
	if err != nil {
		if len(errMsg) > 1 {
			return fmt.Errorf("%v, error output:\n %v", err.Error(), string(errMsg))
		}
	}
	return err
}

func (m *Module) ApplyCommon() error {
	rn, err := executor.NewExecutor(m.codeDir)
	if err != nil {
		log.Debug(err.Error())
		return err
	}
	rn.Env = append(rn.Env, fmt.Sprintf("TF_PLUGIN_CACHE_DIR=%v", config.Global.PluginsCacheDir))
	rn.LogLabels = []string{
		m.InfraName(),
		m.Name(),
		"apply",
	}

	var cmd = ""
	if m.preHook != nil && m.preHook.OnApply {
		cmd = "./pre_hook.sh && "
	}
	cmd += fmt.Sprintf("%[1]s init && %[1]s apply -auto-approve", terraformBin)
	if m.postHook != nil && m.postHook.OnApply {
		cmd += " && ./post_hook.sh"
	}
	var errMsg []byte
	m.applyOutput, errMsg, err = rn.Run(cmd)
	if err != nil {
		if len(errMsg) > 1 {
			return fmt.Errorf("%v, error output:\n %v", err.Error(), string(errMsg))
		}
	}
	return err
}

// Apply module.
func (m *Module) Apply() error {
	err := m.InitCommon()
	if err != nil {
		return err
	}
	return m.ApplyCommon()
}

// Output module.
func (m *Module) Output() (string, error) {
	rn, err := executor.NewExecutor(m.codeDir)
	if err != nil {
		log.Debug(err.Error())
		return "", err
	}
	rn.Env = append(rn.Env, fmt.Sprintf("TF_PLUGIN_CACHE_DIR=%v", config.Global.PluginsCacheDir))
	rn.LogLabels = []string{
		m.InfraName(),
		m.Name(),
		"plan",
	}
	var cmd = ""
	cmd += fmt.Sprintf("%s output", terraformBin)

	var errMsg []byte
	res, errMsg, err := rn.Run(cmd)

	if err != nil {
		if len(errMsg) > 1 {
			return "", fmt.Errorf("%v, error output:\n %v", err.Error(), string(errMsg))
		}
	}
	return string(res), err
}

// Plan module.
func (m *Module) Plan() error {
	rn, err := executor.NewExecutor(m.codeDir)
	if err != nil {
		log.Debug(err.Error())
		return err
	}
	rn.Env = append(rn.Env, fmt.Sprintf("TF_PLUGIN_CACHE_DIR=%v", config.Global.PluginsCacheDir))
	rn.LogLabels = []string{
		m.InfraName(),
		m.Name(),
		"plan",
	}
	var cmd = ""
	if m.preHook != nil && m.preHook.OnPlan {
		cmd = "./pre_hook.sh && "
	}
	cmd += fmt.Sprintf("%[1]s init && %[1]s plan", terraformBin)

	if m.postHook != nil && m.postHook.OnPlan {
		cmd += " && ./post_hook.sh"
	}
	planOutput, errMsg, err := rn.Run(cmd)
	if err != nil {
		if len(errMsg) > 1 {
			return fmt.Errorf("%v, error output:\n %v", err.Error(), string(errMsg))
		}
		return err
	}
	fmt.Printf("%v\n", string(planOutput))
	return nil
}

// Destroy module.
func (m *Module) Destroy() error {
	rn, err := executor.NewExecutor(m.codeDir)
	rn.Env = append(rn.Env, fmt.Sprintf("TF_PLUGIN_CACHE_DIR=%v", config.Global.PluginsCacheDir))
	if err != nil {
		log.Debug(err.Error())
		return err
	}
	rn.LogLabels = []string{
		m.InfraName(),
		m.Name(),
		"destroy",
	}
	var cmd = ""
	if m.preHook != nil && m.preHook.OnDestroy {
		cmd = "./pre_hook.sh && "
	}
	cmd += fmt.Sprintf("%[1]s init && %[1]s destroy -auto-approve", terraformBin)

	if m.postHook != nil && m.postHook.OnDestroy {
		cmd += " && ./post_hook.sh"
	}

	_, errMsg, err := rn.Run(cmd)
	if err != nil {
		if len(errMsg) > 1 {
			return fmt.Errorf("%v, error output:\n %v", err.Error(), string(errMsg))
		}
	}
	return err
}

// Key return uniq module index (string key for maps).
func (m *Module) Key() string {
	return fmt.Sprintf("%v.%v", m.InfraName(), m.name)
}

// CodeDir return path to module code directory.
func (m *Module) CodeDir() string {
	return m.codeDir
}

// UpdateProjectRuntimeDataCommon update project runtime dataset, adds module outputs.
// TODO: get module outputs and write to project runtime dataset. Now this function is only for printer's module interface.
func (m *Module) UpdateProjectRuntimeDataCommon(p *project.Project) error {
	return nil
}

// ReplaceMarkers replace all templated markers with values.
func (m *Module) ReplaceMarkersCommon(inheritedModule project.Module) error {
	if m.preHook != nil {
		err := project.ScanMarkers(&m.preHook.Command, m.RemoteStatesScanner, inheritedModule)
		if err != nil {
			return err
		}
	}
	if m.postHook != nil {
		err := project.ScanMarkers(&m.postHook.Command, m.RemoteStatesScanner, inheritedModule)
		if err != nil {
			return err
		}
	}
	return nil
}
