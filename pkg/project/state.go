package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"github.com/apex/log"
	"github.com/shalb/cluster.dev/pkg/colors"
	"github.com/shalb/cluster.dev/pkg/config"
	"github.com/shalb/cluster.dev/pkg/utils"
)

func (sp *StateProject) UpdateUnit(mod Unit) {
	sp.StateMutex.Lock()
	defer sp.StateMutex.Unlock()
	sp.Units[mod.Key()] = mod
	sp.ChangedUnits[mod.Key()] = mod
}

func (sp *StateProject) DeleteUnit(mod Unit) {
	delete(sp.Units, mod.Key())
}

type StateProject struct {
	Project
	LoaderProjectPtr *Project
	ChangedUnits     map[string]Unit
}

func (p *Project) SaveState() error {
	p.StateMutex.Lock()
	defer p.StateMutex.Unlock()
	st := stateData{
		CdevVersion: config.Global.Version,
		Markers:     p.Markers,
		Units:       map[string]interface{}{},
	}
	for key, mod := range p.Units {
		st.Units[key] = mod.GetState()
	}
	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent(" ", " ")
	err := encoder.Encode(st)
	if err != nil {
		return fmt.Errorf("saving project state: %v", err.Error())
	}
	if p.StateBackendName != "" {
		sBk, ok := p.Backends[p.StateBackendName]
		if !ok {
			return fmt.Errorf("lock state: state backend '%v' does not found", p.StateBackendName)
		}
		return sBk.WriteState(buffer.String())
	}
	return ioutil.WriteFile(config.Global.StateLocalFileName, buffer.Bytes(), fs.ModePerm)
}

type stateData struct {
	CdevVersion string                 `json:version`
	Markers     map[string]interface{} `json:"markers"`
	Units       map[string]interface{} `json:"units"`
}

func (p *Project) LockState() error {
	if p.StateBackendName != "" {
		sBk, ok := p.Backends[p.StateBackendName]
		if !ok {
			return fmt.Errorf("lock state: state backend '%v' does not found", p.StateBackendName)
		}
		return sBk.LockState()
	}
	_, err := ioutil.ReadFile(config.Global.StateLocalLockFile)
	if err == nil {
		return fmt.Errorf("state is locked by another process")
	}
	err = ioutil.WriteFile(config.Global.StateLocalLockFile, []byte{}, os.ModePerm)
	return err
}

func (p *Project) UnLockState() error {
	if p.StateBackendName != "" {
		sBk, ok := p.Backends[p.StateBackendName]
		if !ok {
			return fmt.Errorf("lock state: state backend '%v' does not found", p.StateBackendName)
		}
		return sBk.UnlockState()
	}
	return os.Remove(config.Global.StateLocalLockFile)
}

func (p *Project) GetState() ([]byte, error) {
	var stateStr string
	var err error
	var loadedStateFile []byte
	if p.StateBackendName != "" {
		sBk, ok := p.Backends[p.StateBackendName]
		if !ok {
			return nil, fmt.Errorf("get remote state data: state backend '%v' does not found", p.StateBackendName)
		}
		stateStr, err = sBk.ReadState()
		if err != nil {
			return nil, fmt.Errorf("get remote state data: %w", err)
		}
		loadedStateFile = []byte(stateStr)
	} else {
		loadedStateFile, err = ioutil.ReadFile(config.Global.StateLocalFileName)
		if err != nil {
			return nil, fmt.Errorf("get local state data: read file: %w", err)
		}
	}
	return loadedStateFile, nil
}

func (p *Project) PullState() error {
	loadedStateFile, err := p.GetState()
	if err != nil {
		return fmt.Errorf("backup state: %w", err)
	}
	bkFileName := filepath.Join(config.Global.WorkingDir, "cdev.state")
	log.Infof("Pulling state file: %v", bkFileName)
	return ioutil.WriteFile(bkFileName, loadedStateFile, 0660)
}

func (p *Project) BackupState() error {
	loadedStateFile, err := p.GetState()
	if err != nil {
		return fmt.Errorf("backup state: %w", err)
	}
	const layout = "20060102150405"
	bkFileName := filepath.Join(config.Global.WorkingDir, fmt.Sprintf("cdev.state.backup.%v", time.Now().Format(layout)))
	log.Infof("Backuping state file: %v", bkFileName)
	return ioutil.WriteFile(bkFileName, loadedStateFile, 0660)
}

func (p *Project) LoadState() (*StateProject, error) {
	if _, err := os.Stat(config.Global.StateCacheDir); os.IsNotExist(err) {
		err := os.Mkdir(config.Global.StateCacheDir, 0755)
		if err != nil {
			return nil, fmt.Errorf("load state: create state cache dir: %w", err)
		}
	}
	err := removeDirContent(config.Global.StateCacheDir)
	if err != nil {
		return nil, fmt.Errorf("load state: remove state cache dir: %w", err)
	}

	stateD := stateData{
		Markers: make(map[string]interface{}),
	}

	loadedStateFile, err := p.GetState()
	if err == nil {
		err = utils.JSONDecode(loadedStateFile, &stateD)
		if err != nil {
			return nil, fmt.Errorf("load state: %w", err)
		}
	}

	statePrj := StateProject{
		Project: Project{
			name:             p.Name(),
			secrets:          p.secrets,
			configData:       p.configData,
			configDataFile:   p.configDataFile,
			objects:          p.objects,
			Units:            make(map[string]Unit),
			Markers:          stateD.Markers,
			Stacks:           make(map[string]*Stack),
			Backends:         p.Backends,
			CodeCacheDir:     config.Global.StateCacheDir,
			StateBackendName: p.StateBackendName,
		},
		LoaderProjectPtr: p,
		ChangedUnits:     make(map[string]Unit),
	}

	if statePrj.Markers == nil {
		statePrj.Markers = make(map[string]interface{})
	}
	// for key, m := range p.Markers {
	// 	statePrj.Markers[key] = m
	// }
	utils.JSONCopy(p.Markers, statePrj.Markers)
	for mName, mState := range stateD.Units {
		log.Debugf("Loading unit from state: %v", mName)

		if mState == nil {
			continue
		}
		unit, err := NewUnitFromState(mState.(map[string]interface{}), mName, &statePrj)
		if err != nil {
			return nil, fmt.Errorf("loading unit from state: %v", err.Error())
		}
		statePrj.Units[mName] = unit
		unit.UpdateProjectRuntimeData(&statePrj.Project)
	}
	err = statePrj.prepareUnits()
	if err != nil {
		return nil, err
	}
	p.OwnState = &statePrj
	return &statePrj, nil
}

func (sp *StateProject) CheckUnitChanges(unit Unit) (string, Unit) {
	unitInState, exists := sp.Units[unit.Key()]
	if !exists {
		return utils.Diff(nil, unit.GetDiffData(), true), nil
	}

	diffData := unit.GetDiffData()
	stateDiffData := unitInState.GetDiffData()
	// m, _ := utils.JSONEncodeString(diffData)
	// log.Warnf("Diff data: %v", m)
	// sm, _ := utils.JSONEncodeString(stateDiffData)
	// log.Warnf("State diff data: %v", sm)
	// mr, _ := utils.JSONEncodeString(unitInState.Project().Markers)
	// log.Warnf("markers: %v", mr)
	// smr, _ := utils.JSONEncodeString(unitInState.Project().Markers)
	// log.Warnf("state markers: %v", smr)
	df := utils.Diff(stateDiffData, diffData, true)
	if len(df) > 0 {
		return df, unitInState
	}
	for _, u := range unit.RequiredUnits() {
		if sp.checkUnitChangesRecursive(u) {
			return colors.Fmt(colors.Yellow).Sprintf("+/- There are changes in the unit dependencies."), unitInState
		}
	}
	return "", unitInState
}

func (sp *StateProject) checkUnitChangesRecursive(unit Unit) bool {
	if unit.WasApplied() {
		return true
	}
	unitInState, exists := sp.Units[unit.Key()]
	if !exists {
		return true
	}
	diffData := unit.GetDiffData()

	df := utils.Diff(unitInState.GetDiffData(), diffData, true)
	if len(df) > 0 {
		return true
	}
	for _, u := range unit.RequiredUnits() {
		if _, exists := sp.ChangedUnits[u.Key()]; exists {
			return true
		}
		if sp.checkUnitChangesRecursive(u) {
			return true
		}
	}
	return false
}
