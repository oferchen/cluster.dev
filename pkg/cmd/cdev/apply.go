package cdev

import (
	"github.com/shalb/cluster.dev/pkg/config"
	"github.com/shalb/cluster.dev/pkg/project"
	"github.com/spf13/cobra"
)

// planCmd represents the plan command
var applyCmd = &cobra.Command{
	Use:           "apply",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Deploys or updates infrastructure according to project configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		project, err := project.LoadProjectFull()

		if err != nil {
			return NewCmdErr(project, "apply", err)
		}
		err = project.LockState()
		if err != nil {
			return NewCmdErr(project, "apply", err)
		}
		err = project.Apply()
		if err != nil {
			return NewCmdErr(project, "apply", err)
		}
		err = project.PrintOutputs()
		if err != nil {
			return NewCmdErr(project, "apply", err)
		}
		project.UnLockState()
		return NewCmdErr(project, "apply", nil)
	},
}

func init() {
	rootCmd.AddCommand(applyCmd)
	applyCmd.Flags().BoolVar(&config.Global.IgnoreState, "ignore-state", false, "Apply even if the state has not changed.")
	applyCmd.Flags().BoolVar(&config.Global.Force, "force", false, "Skip interactive approval.")
}
