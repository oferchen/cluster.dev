package tfmodule

import (
	"fmt"
	"io/fs"

	"github.com/apex/log"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/shalb/cluster.dev/pkg/hcltools"
	"github.com/shalb/cluster.dev/pkg/project"
	"github.com/shalb/cluster.dev/pkg/units/shell/terraform/base"
)

type Unit struct {
	base.Unit
	OutputRaw string                 `yaml:"-" json:"output"`
	Inputs    map[string]interface{} `yaml:"-" json:"inputs"`
	UnitKind  string                 `yaml:"-" json:"type"`
	StateData *Unit                  `yaml:"-" json:"-"`
}

func (u *Unit) KindKey() string {
	return unitKind
}

func (u *Unit) genMainCodeBlock() ([]byte, error) {
	f := hclwrite.NewEmptyFile()
	rootBody := f.Body()

	for key, val := range u.Inputs {
		dataBlock := rootBody.AppendNewBlock("output", []string{key})
		dataBody := dataBlock.Body()
		hclVal, err := hcltools.InterfaceToCty(val)
		if err != nil {
			return nil, err
		}
		dataBody.SetAttributeValue("value", hclVal)

		for hash, marker := range u.ProjectPtr.UnitLinks.ByLinkTypes(base.RemoteStateLinkType).Map() {

			refStr := base.DependencyToRemoteStateRef(marker)
			hcltools.ReplaceStingMarkerInBody(dataBody, hash, refStr)
		}
	}
	// log.Errorf("genMainCodeBlock: %v, %v", string(f.Bytes()))
	return f.Bytes(), nil
}

func (u *Unit) ReadConfig(spec map[string]interface{}, stack *project.Stack) error {

	modType, ok := spec["type"].(string)
	if !ok {
		return fmt.Errorf("Incorrect unit type")
	}
	if modType != u.KindKey() {
		return fmt.Errorf("Incorrect unit type")
	}
	mInputs, ok := spec["inputs"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("Incorrect unit inputs")
	}
	u.Inputs = mInputs
	return nil
}

func (u *Unit) ScanData(scanner project.MarkerScanner) error {
	// log.Infof("Scan inputs: %v", u.Inputs)
	err := project.ScanMarkers(u.Inputs, scanner, u)
	if err != nil {
		return err
	}
	return nil
}

// Prepare scan all markers in unit, and build project unit links, and unit dependencies.
func (u *Unit) Prepare() error {
	err := u.Unit.Prepare()
	if err != nil {
		return err
	}
	err = u.ScanData(project.OutputsScanner)
	if err != nil {
		return err
	}
	return u.ScanData(u.RemoteStatesScanner)
}

// Build generate all terraform code for project.
func (u *Unit) Build() error {
	// Save state before outputs replacing.
	u.StateData = u.GetStateUnit()
	// Replace outputs.
	err := u.ScanData(project.OutputsReplacer)
	if err != nil {
		return err
	}
	mainBlock, err := u.genMainCodeBlock()
	if err != nil {
		log.Debug(err.Error())
		return err
	}
	if err = u.CreateFiles.Add("main.tf", string(mainBlock), fs.ModePerm); err != nil {
		return err
	}

	u.CreateFiles.Delete("init.tf")
	return u.Unit.Build()
}

func (u *Unit) Apply() (err error) {
	err = u.Unit.Apply()
	if err != nil {
		return
	}
	outputs, err := u.Output()
	if err != nil {
		return
	}
	u.OutputRaw = outputs
	u.StateData.OutputRaw = outputs
	// log.Warnf("Printer outputs: %v", u.ProjectPtr.UnitLinks.ByTargetUnit(u))
	return
}

// UpdateProjectRuntimeData update project runtime dataset, adds printer unit outputs.
func (u *Unit) UpdateProjectRuntimeData(p *project.Project) error {
	p.RuntimeDataset.PrintersOutputs = append(p.RuntimeDataset.PrintersOutputs, project.PrinterOutput{Name: u.Key(), Output: u.OutputRaw})
	// log.Warnf("Printer UpdateProjectRuntimeData: %v", u.OutputRaw)
	return u.Unit.UpdateProjectRuntimeData(p)
}