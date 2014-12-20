package partition

import (
)

type GrubBootLoader struct {
	partition *Partition
}

func (g *GrubBootLoader) Name() string {
	return "grub"
}

func (g *GrubBootLoader) Installed() bool {
	// crude heuristic
	err := FileExists("/boot/grub/grub.cfg")

	if err == nil {
		return true
	}

	return false
}

// Make the Grub bootloader switch rootfs's.
//
// Approach:
//
// Re-install grub each time the rootfs is toggled by running
// grub-install chrooted into the other rootfs. Also update the grub
// configuration.
func (g *GrubBootLoader) ToggleRootFS(p *Partition) (err error) {
	var args []string
	var other *BlockDevice

	// save
	g.partition = p

	other = p.OtherRootPartition()

	args = append(args, "grub-install")
	args = append(args, other.parentName)

	// install grub
	err = p.RunInChroot(args)
	if err != nil {
		return err
	}

	args = nil
	args = append(args, "update-grub")

	// create the grub config
	err = p.RunInChroot(args)

	return err
}

func (g *GrubBootLoader) GetAllBootVars() (vars []string, err error) {
	// FIXME: 'grub-editenv list'
	return vars, err
}

func (g *GrubBootLoader) GetBootVar(name string) (value string) {
	// FIXME: 'grub-editenv list|grep $name'
	return value
}

func (g *GrubBootLoader) SetBootVar(name, value string) (err error) {
	// FIXME: 'grub-editenv set name=value'
	return err
}

func (g *GrubBootLoader) ClearBootVar(name string) (currentValue string, err error) {
	// FIXME: 'grub-editenv unset name'
	return currentValue, err
}

func (g *GrubBootLoader) GetNextBootRootLabel() (label string) {
	// FIXME
	return label
}

func (g *GrubBootLoader) GetCurrentBootRootLabel() (label string) {
	// FIXME
	return label
}