// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2016 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package builtin

import (
	"fmt"
	"path/filepath"

	"github.com/ubuntu-core/snappy/interfaces"
	"github.com/ubuntu-core/snappy/snap"
)

type evalSymlinksFn func(string) (string, error)

// evalSymlinks is either filepath.EvalSymlinks or a mocked function for
// applicable for testing.
var evalSymlinks = filepath.EvalSymlinks

type commonInterface struct {
	name                  string
	connectedPlugAppArmor string
	connectedPlugSecComp  string
	reservedForOS         bool
	autoConnect           bool
}

// Name returns the interface name.
func (iface *commonInterface) Name() string {
	return iface.name
}

// SanitizeSlot checks and possibly modifies a slot.
//
// If the reservedForOS flag is set then only slots on the "ubuntu-core" snap
// are allowed.
func (iface *commonInterface) SanitizeSlot(slot *interfaces.Slot) error {
	if iface.Name() != slot.Interface {
		panic(fmt.Sprintf("slot is not of interface %q", iface.Name()))
	}
	if iface.reservedForOS && slot.Snap.Type != snap.TypeOS {
		return fmt.Errorf("%s slots are reserved for the operating system snap", iface.name)
	}
	return nil
}

// SanitizePlug checks and possibly modifies a plug.
func (iface *commonInterface) SanitizePlug(plug *interfaces.Plug) error {
	if iface.Name() != plug.Interface {
		panic(fmt.Sprintf("plug is not of interface %q", iface.Name()))
	}
	// NOTE: currently we don't check anything on the plug side.
	return nil
}

// PermanentPlugSnippet returns the snippet of text for the given security
// system that is used during the whole lifetime of affected applications,
// whether the plug is connected or not.
//
// Plugs don't get any permanent security snippets.
func (iface *commonInterface) PermanentPlugSnippet(plug *interfaces.Plug, securitySystem interfaces.SecuritySystem) ([]byte, error) {
	switch securitySystem {
	case interfaces.SecurityAppArmor, interfaces.SecuritySecComp, interfaces.SecurityDBus, interfaces.SecurityUDev:
		return nil, nil
	default:
		return nil, interfaces.ErrUnknownSecurity
	}
}

// ConnectedPlugSnippet returns the snippet of text for the given security
// system that is used by affected application, while a specific connection
// between a plug and a slot exists.
//
// Connected plugs get the static seccomp and apparmor blobs defined by the
// instance variables.  They are not really connection specific in this case.
func (iface *commonInterface) ConnectedPlugSnippet(plug *interfaces.Plug, slot *interfaces.Slot, securitySystem interfaces.SecuritySystem) ([]byte, error) {
	switch securitySystem {
	case interfaces.SecurityAppArmor:
		return []byte(iface.connectedPlugAppArmor), nil
	case interfaces.SecuritySecComp:
		return []byte(iface.connectedPlugSecComp), nil
	case interfaces.SecurityDBus, interfaces.SecurityUDev:
		return nil, nil
	default:
		return nil, interfaces.ErrUnknownSecurity
	}
}

// PermanentSlotSnippet returns the snippet of text for the given security
// system that is used during the whole lifetime of affected applications,
// whether the slot is connected or not.
//
// Slots don't get any permanent security snippets.
func (iface *commonInterface) PermanentSlotSnippet(slot *interfaces.Slot, securitySystem interfaces.SecuritySystem) ([]byte, error) {
	switch securitySystem {
	case interfaces.SecurityAppArmor, interfaces.SecuritySecComp, interfaces.SecurityDBus, interfaces.SecurityUDev:
		return nil, nil
	default:
		return nil, interfaces.ErrUnknownSecurity
	}
}

// ConnectedSlotSnippet returns the snippet of text for the given security
// system that is used by affected application, while a specific connection
// between a plug and a slot exists.
//
// Slots don't get any per-connection security snippets.
func (iface *commonInterface) ConnectedSlotSnippet(plug *interfaces.Plug, slot *interfaces.Slot, securitySystem interfaces.SecuritySystem) ([]byte, error) {
	switch securitySystem {
	case interfaces.SecurityAppArmor, interfaces.SecuritySecComp, interfaces.SecurityDBus, interfaces.SecurityUDev:
		return nil, nil
	default:
		return nil, interfaces.ErrUnknownSecurity
	}
}

// AutoConnect returns true if plugs and slots should be implicitly
// auto-connected when an unambiguous connection candidate is available.
func (iface *commonInterface) AutoConnect() bool {
	return iface.autoConnect
}
