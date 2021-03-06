/////////////////////////////////////////////////////////////////////////////////
//
// capture_manager.go
//
// Written by Lorenz Breidenbach lob@open.ch, December 2015
// Copyright (c) 2015 Open Systems AG, Switzerland
// All Rights Reserved.
//
/////////////////////////////////////////////////////////////////////////////////

package goProbe

import (
    "fmt"
    "sync"
    "time"

    "OSAG/goDB"
)

// TaggedAggFlowMap represents an aggregated
// flow map tagged with CaptureStats and an
// an interface name.
//
// Used by CaptureManager to return the results of
// RotateAll() and Update().
type TaggedAggFlowMap struct {
    Map   goDB.AggFlowMap
    Stats CaptureStats
    Iface string
}

// CaptureManager manages a set of Capture instances.
// Each interface can be associated with up to one Capture.
type CaptureManager struct {
    captures map[string]*Capture
}

// NewCaptureManager creates a new CaptureManager and
// returns a pointer to it.
func NewCaptureManager() *CaptureManager {
    return &CaptureManager{
        make(map[string]*Capture),
    }
}

func (cm *CaptureManager) ifaceNames() []string {
    ifaces := make([]string, 0, len(cm.captures))
    for iface, _ := range cm.captures {
        ifaces = append(ifaces, iface)
    }
    return ifaces
}

func (cm *CaptureManager) enable(ifaces map[string]CaptureConfig) {
    var rg RunGroup

    for iface, config := range ifaces {
        if _, exists := cm.captures[iface]; exists {
            capture, config := cm.captures[iface], config
            rg.Run(func() {
                capture.Update(config)
            })
        } else {
            capture := NewCapture(iface, config)
            cm.captures[iface] = capture

            SysLog.Info(fmt.Sprintf("Added interface '%s' to capture list.", iface))

            rg.Run(func() {
                capture.Enable()
            })
        }
    }

    rg.Wait()
}

// EnableAll attempts to enable all managed Capture instances.
//
// Returns once all instances have been enabled.
// Note that each attempt may fail, for example if the interface
// that a Capture is supposed to monitor ceases to exist. Use
// StateAll() to find out wheter the Capture instances encountered
// an error.
func (cm *CaptureManager) EnableAll() {
    t0 := time.Now()

    var rg RunGroup

    for _, capture := range cm.captures {
        capture := capture
        rg.Run(func() {
            capture.Enable()
        })
    }

    rg.Wait()

    SysLog.Debug(fmt.Sprintf("Completed interface capture check in %s", time.Now().Sub(t0)))
}

func (cm *CaptureManager) disable(ifaces []string) {
    var rg RunGroup

    for _, iface := range ifaces {
        iface := iface
        rg.Run(func() {
            cm.captures[iface].Disable()
        })
    }
    rg.Wait()
}

// DisableAll disables all managed Capture instances.
//
// Returns once all instances have been disabled.
// The instances are not deleted, so you may later enable them again;
// for example, by calling EnableAll().
func (cm *CaptureManager) DisableAll() {
    t0 := time.Now()

    cm.disable(cm.ifaceNames())

    SysLog.Info(fmt.Sprintf("Disabled all captures in %s", time.Now().Sub(t0)))
}

// Update attempts to enable all Capture instances given by
// ifaces. If an instance doesn't exist, it will be created.
// If an instance has encountered an error or an instance's configuration
// differs from the one specified in ifaces, it will be re-enabled.
// Finally, if the CaptureManager manages an instance for an iface that does
// not occur in ifaces, the following actions are performed on the instance:
// (1) the instance will be disabled,
// (2) the instance will be rotated,
// (3) the resulting flow data will be sent over returnChan,
// (tagged with the interface name and stats),
// (4) the instance will be closed,
// and (5) the instance will be completely removed from the CaptureManager.
//
// Returns once all the above actions have been completed.
func (cm *CaptureManager) Update(ifaces map[string]CaptureConfig, returnChan chan TaggedAggFlowMap) {
    t0 := time.Now()

    ifaceSet := make(map[string]struct{})
    for iface := range ifaces {
        ifaceSet[iface] = struct{}{}
    }

    // Contains the names of all interfaces we are shutting down and deleting.
    var disableIfaces []string
    for iface, _ := range cm.captures {
        if _, exists := ifaceSet[iface]; !exists {
            disableIfaces = append(disableIfaces, iface)
        }
    }

    var rg RunGroup
    // disableIfaces and ifaces are disjunct, so we can run these in parallel.
    rg.Run(func() {
        cm.disable(disableIfaces)
    })
    rg.Run(func() {
        cm.enable(ifaces)
    })
    rg.Wait()

    for _, iface := range disableIfaces {
        iface, capture := iface, cm.captures[iface]
        rg.Run(func() {
            aggFlowMap, stats := capture.Rotate()
            returnChan <- TaggedAggFlowMap{
                aggFlowMap,
                stats,
                iface,
            }

            capture.Close()
        })

        delete(cm.captures, iface)
        SysLog.Info(fmt.Sprintf("Deleted interface '%s' from capture list.", iface))
    }
    rg.Wait()

    SysLog.Debug(fmt.Sprintf("Updated interface list in %s", time.Now().Sub(t0)))
}

// StatusAll() returns the statuses of all managed Capture instances.
func (cm *CaptureManager) StatusAll() map[string]CaptureStatus {
    statusmapMutex := sync.Mutex{}
    statusmap := make(map[string]CaptureStatus)

    var rg RunGroup
    for iface, capture := range cm.captures {
        iface, capture := iface, capture
        rg.Run(func() {
            status := capture.Status()
            statusmapMutex.Lock()
            statusmap[iface] = status
            statusmapMutex.Unlock()
        })
    }
    rg.Wait()

    return statusmap
}

// RotateAll() returns the state of all managed Capture instances.
//
// The resulting TaggedAggFlowMaps will be sent over returnChan and
// be tagged with the given timestamp.
func (cm *CaptureManager) RotateAll(returnChan chan TaggedAggFlowMap) {
    t0 := time.Now()

    var rg RunGroup

    for iface, capture := range cm.captures {
        iface, capture := iface, capture
        rg.Run(func() {
            aggFlowMap, stats := capture.Rotate()
            returnChan <- TaggedAggFlowMap{
                aggFlowMap,
                stats,
                iface,
            }
        })
    }
    rg.Wait()

    SysLog.Debug(fmt.Sprintf("Completed rotation of all captures in %s", time.Now().Sub(t0)))
}

// CloseAll() closes and deletes all Capture instances managed by the
// CaptureManager
func (cm *CaptureManager) CloseAll() {
    var rg RunGroup

    for _, capture := range cm.captures {
        capture := capture
        rg.Run(func() {
            capture.Close()
        })
    }

    cm.captures = make(map[string]*Capture)

    rg.Wait()
}
