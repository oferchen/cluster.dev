package project

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/apex/log"
	"github.com/olekukonko/tablewriter"
	"github.com/shalb/cluster.dev/pkg/colors"
	"github.com/shalb/cluster.dev/pkg/config"
	"github.com/shalb/cluster.dev/pkg/utils"
)

// CreateMarker generate hash string for template markers.
func CreateMarker(markerPath, dataForHash string) string {
	hash := utils.Md5(dataForHash)
	return fmt.Sprintf("%s.%s.%s", hash, markerPath, hash)
}

func printVersion() string {
	return config.Global.Version
}

func removeDirContent(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		err = os.RemoveAll(filepath.Join(dir, name))
		if err != nil {
			return err
		}
	}
	return nil
}

func findModule(module Module, modsList map[string]Module) *Module {
	mod, exists := modsList[fmt.Sprintf("%s.%s", module.InfraName(), module.Name())]
	// log.Printf("Check Mod: %s, exists: %v, list %v", name, exists, modsList)
	if !exists {
		return nil
	}
	return &mod
}

// ScanMarkers use marker scanner function to replace templated markers.
func ScanMarkers(data interface{}, procFunc MarkerScanner, module Module) error {
	if data == nil {
		return nil
	}
	out := reflect.ValueOf(data)
	if out.Kind() == reflect.Ptr && !out.IsNil() {
		out = out.Elem()
	}
	if out.IsNil() {
		return nil
	}
	switch out.Kind() {
	case reflect.Slice:
		for i := 0; i < out.Len(); i++ {
			elem := out.Index(i)
			if elem.Kind() != reflect.Interface && elem.Kind() != reflect.Ptr {
				val, err := procFunc(elem, module)
				if err != nil {
					return err
				}
				if val.Kind() != elem.Kind() {
					log.Fatal("ScanMarkers: type conversion error")
				}
				out.Index(i).Set(val)
				continue
			}
			if elem.Elem().Kind() == reflect.String {
				val, err := procFunc(elem, module)
				if err != nil {
					return err
				}
				out.Index(i).Set(val)
			} else {
				err := ScanMarkers(out.Index(i).Interface(), procFunc, module)
				if err != nil {
					return err
				}
			}
		}
	case reflect.Map:
		for _, key := range out.MapKeys() {
			elem := out.MapIndex(key)
			if elem.Kind() != reflect.Interface && elem.Kind() != reflect.Ptr {
				val, err := procFunc(elem, module)
				if err != nil {
					return err
				}
				if val.Kind() != elem.Kind() {
					log.Fatal("ScanMarkers: type conversion error")
				}
				out.SetMapIndex(key, val)
				continue
			}
			if elem.Elem().Kind() == reflect.String {
				val, err := procFunc(out.MapIndex(key), module)
				if err != nil {
					return err
				}
				out.SetMapIndex(key, val)
			} else {
				err := ScanMarkers(elem.Interface(), procFunc, module)
				if err != nil {
					return err
				}
			}
		}
	case reflect.Struct:
		for i := 0; i < out.NumField(); i++ {
			if out.Field(i).Kind() == reflect.String {
				val, err := procFunc(reflect.ValueOf(out.Field(i).Interface()), module)
				if err != nil {
					return err
				}
				out.Field(i).Set(val)
			} else {
				err := ScanMarkers(out.Field(i).Interface(), procFunc, module)
				if err != nil {
					return err
				}
			}
		}
	case reflect.Interface:
		log.Warnf("%v", out)
		if reflect.TypeOf(out.Interface()).Kind() == reflect.String {
			if !out.CanSet() {
				log.Fatal("Internal error: can't set interface field.")
			}
			val, err := procFunc(out, module)
			if err != nil {
				return err
			}
			out.Set(val)
		}
	case reflect.String:
		val, err := procFunc(out, module)
		if err != nil {
			return err
		}
		if !out.CanSet() {
			log.Fatalf("Internal error: can't set string field.")
		}
		out.Set(val)
	default:
		log.Debugf("Unknown type: %v", out.Type())
	}
	return nil
}

func ConvertToTfVarName(name string) string {
	reg, err := regexp.Compile("[^a-zA-Z0-9]+")
	if err != nil {
		log.Fatal(err.Error())
	}
	processedString := reg.ReplaceAllString(name, "_")
	return strings.ToLower(processedString)
}

func ConvertToShellVarName(name string) string {
	return strings.ToUpper(ConvertToTfVarName(name))
}

func ConvertToShellVar(name string) string {
	return fmt.Sprintf("${%s}", ConvertToShellVarName(name))
}

func BuildDep(m Module, dep *Dependency) error {
	if dep.Module == nil {

		if dep.ModuleName == "" || dep.InfraName == "" {
			return fmt.Errorf("Empty dependency in module '%v.%v'", m.InfraName(), m.Name())
		}
		depMod, exists := m.ProjectPtr().Modules[fmt.Sprintf("%v.%v", dep.InfraName, dep.ModuleName)]
		if !exists {
			return fmt.Errorf("Error in module '%v.%v' dependency, target '%v.%v' does not exist", m.InfraName(), m.Name(), dep.InfraName, dep.ModuleName)
		}
		dep.Module = depMod
	}
	return nil
}

// BuildModuleDeps check all dependencies and add module pointer.
func BuildModuleDeps(m Module) error {
	for _, dep := range *m.Dependencies() {
		err := BuildDep(m, dep)
		if err != nil {
			log.Debug(err.Error())
			return err
		}
	}
	return nil
}

func ProjectsFilesExists() bool {
	project := NewEmptyProject()
	_ = project.readManifests()
	if project.configDataFile != nil || len(project.objectsFiles) > 0 {
		return true
	}
	return false
}

func showPlanResults(deployList, updateList, destroyList, unchangedList []string) {
	fmt.Println(colors.Fmt(colors.WhiteBold).Sprint("Plan results:"))

	if len(deployList)+len(updateList)+len(destroyList) == 0 {
		fmt.Println(colors.Fmt(colors.WhiteBold).Sprint("No changes, nothing to do."))
		return
	}
	table := tablewriter.NewWriter(os.Stdout)

	headers := []string{}
	modulesTable := []string{}

	var deployString, updateString, destroyString, unchangedString string
	for i, modName := range deployList {
		if i != 0 {
			deployString += "\n"
		}
		deployString += colors.Fmt(colors.Green).Sprint(modName)
	}
	for i, modName := range updateList {
		if i != 0 {
			updateString += "\n"
		}
		updateString += colors.Fmt(colors.Yellow).Sprint(modName)
	}
	for i, modName := range destroyList {
		if i != 0 {
			destroyString += "\n"
		}
		destroyString += colors.Fmt(colors.Red).Sprint(modName)
	}
	for i, modName := range unchangedList {
		if i != 0 {
			unchangedString += "\n"
		}
		unchangedString += colors.Fmt(colors.White).Sprint(modName)
	}
	if len(deployList) > 0 {
		headers = append(headers, "Will be deployed")
		modulesTable = append(modulesTable, deployString)
	}
	if len(updateList) > 0 {
		headers = append(headers, "Will be updated")
		modulesTable = append(modulesTable, updateString)
	}
	if len(destroyList) > 0 {
		headers = append(headers, "Will be destroyed")
		modulesTable = append(modulesTable, destroyString)
	}
	if len(unchangedList) > 0 {
		headers = append(headers, "Unchanged")
		modulesTable = append(modulesTable, unchangedString)
	}
	table.SetHeader(headers)
	table.Append(modulesTable)
	table.Render()
}
