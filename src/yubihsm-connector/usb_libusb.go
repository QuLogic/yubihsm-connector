// Copyright 2016-2018 Yubico AB
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build !windows

package main

import (
	"fmt"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/thorduri/go-libusb/usb"
)

var state struct {
	ctx       *usb.Context
	device    *usb.Device
	wendpoint usb.Endpoint
	rendpoint usb.Endpoint

	mtx sync.Mutex
}

func usbopen(cid string) (err error) {
	if state.ctx == nil {
		log.WithField("Correlation-ID", cid).Debug("usb context not yet open")
		state.ctx = usb.NewContext()
		if state.ctx == nil {
			return fmt.Errorf("unable to create a usb context")
		}
	}
	if state.device != nil {
		log.WithField("Correlation-ID", cid).Debug("usb device already open")
		return nil
	}

	var devs []*usb.Device
	devs, err = state.ctx.ListDevices(func(desc *usb.Descriptor) bool {
		if desc.Vendor == 0x1050 && desc.Product == 0x0030 {
			return true
		}
		return false
	})
	if err != nil {
		goto out
	}

	for _, dev := range devs {
		serialnumber := ""
		idx := int(dev.Descriptor.ISerialNumber)
		serialnumber, err = dev.GetStringDescriptor(idx)
		if err != nil {
			dev.Close()
			continue
		}
		fields := log.Fields{
			"Correlation-ID": cid,
			"Device-Serial":  serialnumber,
			"Wanted-Serial":  serial,
		}
		if serial != "" && serial != serialnumber {
			log.WithFields(fields).Debug("Device skipped for non-matching serial")
			dev.Close()
		} else {
			log.WithFields(fields).Debug("Returning a matched device")
			state.device = dev
		}
	}
	if state.device == nil {
		err = fmt.Errorf("device not found")
		goto out
	}
	state.device.ReadTimeout = 0
	state.device.WriteTimeout = 0
	state.device.ControlTimeout = 0

	state.wendpoint, err = state.device.OpenEndpoint(1, 0, 0, 0x1)
	if err != nil {
		goto out
	}

	state.rendpoint, err = state.device.OpenEndpoint(1, 0, 0, 0x81)
	if err != nil {
		goto out
	}

	state.device.ReadTimeout = 1000000
	usbread(cid)
	state.device.ReadTimeout = 0

	return nil

out:
	usbclose(cid)
	return err
}

func usbclose(cid string) {
	if state.device != nil {
		state.device.Close()
		state.device = nil
	}
}

func usbreopen(cid string, why error) (err error) {
	log.WithFields(log.Fields{
		"Correlation-ID": cid,
		"why":            why,
	}).Debug("reopening usb context")

	// If the first request to the connector is a status request,
	// the device context might not have been created yet.
	if state.device != nil {
		if err = state.device.Reset(); err != nil {
			log.WithField(
				"Correlation-ID", cid,
			).WithError(err).Error("unable to reset device")
		}
	}

	usbclose(cid)
	return usbopen(cid)
}

func usbReopen(cid string, why error) (err error) {
	state.mtx.Lock()
	defer state.mtx.Unlock()

	return usbreopen(cid, why)
}

func usbwrite(buf []byte, cid string) (err error) {
	var n int

	if n, err = state.wendpoint.Write(buf); err != nil {
		goto out
	}
	if len(buf)%64 == 0 {
		var empty []byte
		if n, err = state.wendpoint.Write(empty); err != nil {
			goto out
		}
	}

out:
	log.WithFields(log.Fields{
		"Correlation-ID": cid,
		"n":              n,
		"err":            err,
		"len":            len(buf),
		"buf":            buf,
	}).Debug("usb endpoint write")

	return err
}

func usbread(cid string) (buf []byte, err error) {
	var n int

	buf = make([]byte, 8192)
	if n, err = state.rendpoint.Read(buf); err != nil {
		buf = buf[:0]
		goto out
	}
	buf = buf[:n]

out:
	log.WithFields(log.Fields{
		"Correlation-ID": cid,
		"n":              n,
		"err":            err,
		"len":            len(buf),
		"buf":            buf,
	}).Debug("usb endpoint read")

	return buf, err
}

func usbProxy(req []byte, cid string) (resp []byte, err error) {
	state.mtx.Lock()
	defer state.mtx.Unlock()

	if err = usbopen(cid); err != nil {
		return nil, err
	}

	for {
		err = usbwrite(req, cid)
		switch err {
		case usb.ERROR_NO_DEVICE, usb.ERROR_NOT_FOUND:
			if err = usbreopen(cid, err); err != nil {
				return nil, err
			}
			continue
		}

		resp, err = usbread(cid)
		switch err {
		case usb.ERROR_NO_DEVICE, usb.ERROR_NOT_FOUND:
			if err = usbreopen(cid, err); err != nil {
				return nil, err
			}
			continue
		}

		break
	}

	return resp, err
}
