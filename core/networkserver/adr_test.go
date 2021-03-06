// Copyright © 2017 The Things Network
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package networkserver

import (
	"math"
	"runtime"
	"sort"
	"testing"

	pb_broker "github.com/TheThingsNetwork/ttn/api/broker"
	pb_gateway "github.com/TheThingsNetwork/ttn/api/gateway"
	pb_protocol "github.com/TheThingsNetwork/ttn/api/protocol"
	pb_lorawan "github.com/TheThingsNetwork/ttn/api/protocol/lorawan"
	"github.com/TheThingsNetwork/ttn/core/networkserver/device"
	"github.com/TheThingsNetwork/ttn/core/types"
	. "github.com/TheThingsNetwork/ttn/utils/testing"
	"github.com/brocaar/lorawan"
	. "github.com/smartystreets/assertions"
)

func adrInitUplinkMessage() *pb_broker.DeduplicatedUplinkMessage {
	message := &pb_broker.DeduplicatedUplinkMessage{
		Message: new(pb_protocol.Message),
	}
	message.Message.InitLoRaWAN().InitUplink()
	downlink := message.InitResponseTemplate()
	downlink.Message = new(pb_protocol.Message)
	downlink.Message.InitLoRaWAN().InitDownlink()
	message.ProtocolMetadata = &pb_protocol.RxMetadata{Protocol: &pb_protocol.RxMetadata_Lorawan{
		Lorawan: &pb_lorawan.Metadata{
			DataRate: "SF8BW125",
		},
	}}
	message.GatewayMetadata = []*pb_gateway.RxMetadata{
		&pb_gateway.RxMetadata{
			Snr: 10,
		},
	}
	return message
}

func adrInitDownlinkMessage() *pb_broker.DownlinkMessage {
	message := &pb_broker.DownlinkMessage{
		Message: new(pb_protocol.Message),
	}
	message.Message.InitLoRaWAN().InitDownlink()
	return message
}

func buildFrames(fCnts ...int) []*device.Frame {
	sort.Sort(sort.Reverse(sort.IntSlice(fCnts)))
	frames := make([]*device.Frame, 0, len(fCnts))
	for _, fCnt := range fCnts {
		frames = append(frames, &device.Frame{
			FCnt: uint32(fCnt),
			SNR:  float32(math.Floor(math.Sin(float64(fCnt))*100) / 10),
		})
	}
	return frames
}

func TestMaxSNR(t *testing.T) {
	a := New(t)
	a.So(maxSNR(buildFrames()), ShouldEqual, 0)
	a.So(maxSNR(buildFrames(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)), ShouldEqual, 9.8)
}

func TestLossPercentage(t *testing.T) {
	a := New(t)
	a.So(lossPercentage(buildFrames()), ShouldEqual, 0)
	a.So(lossPercentage(buildFrames(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)), ShouldEqual, 0)
	a.So(lossPercentage(buildFrames(1, 2, 3, 4, 5, 6, 7, 8, 9, 11)), ShouldEqual, 9)    // 1/11 missing
	a.So(lossPercentage(buildFrames(1, 2, 3, 4, 5, 6, 7, 8, 9, 12)), ShouldEqual, 17)   // 2/12 missing
	a.So(lossPercentage(buildFrames(1, 2, 3, 6, 7, 8, 9, 12, 13, 14)), ShouldEqual, 29) // 4/14 missing
}

func TestHandleUplinkADR(t *testing.T) {
	a := New(t)
	ns := &networkServer{
		devices: device.NewRedisDeviceStore(GetRedisClient(), "ns-test-handle-uplink-adr"),
	}
	ns.InitStatus()

	defer func() {
		keys, _ := GetRedisClient().Keys("*ns-test-handle-uplink-adr*").Result()
		for _, key := range keys {
			GetRedisClient().Del(key).Result()
		}
	}()

	appEUI := types.AppEUI([8]byte{1})
	devEUI := types.DevEUI([8]byte{1})
	history, _ := ns.devices.Frames(appEUI, devEUI)

	// Setting ADR to true should start collecting frames
	{
		dev := &device.Device{AppEUI: appEUI, DevEUI: devEUI}
		message := adrInitUplinkMessage()
		message.Message.GetLorawan().GetMacPayload().Adr = true
		err := ns.handleUplinkADR(message, dev)
		a.So(err, ShouldBeNil)
		frames, _ := history.Get()
		a.So(frames, ShouldHaveLength, 1)
		a.So(dev.ADR.DataRate, ShouldEqual, "SF8BW125")
	}

	// Resetting ADR to false should empty the frames
	{
		dev := &device.Device{AppEUI: appEUI, DevEUI: devEUI}
		message := adrInitUplinkMessage()
		err := ns.handleUplinkADR(message, dev)
		a.So(err, ShouldBeNil)
		frames, _ := history.Get()
		a.So(frames, ShouldBeEmpty)
	}

	// Setting ADRAckReq to true should set the ACK and schedule a LinkADRReq
	{
		dev := &device.Device{AppEUI: appEUI, DevEUI: devEUI}
		message := adrInitUplinkMessage()
		message.Message.GetLorawan().GetMacPayload().Adr = true
		message.Message.GetLorawan().GetMacPayload().AdrAckReq = true
		err := ns.handleUplinkADR(message, dev)
		a.So(err, ShouldBeNil)
		resMac := message.ResponseTemplate.Message.GetLorawan().GetMacPayload()
		a.So(resMac.Ack, ShouldBeTrue)
		a.So(dev.ADR.SendReq, ShouldBeTrue)
	}
}

func TestHandleDownlinkADR(t *testing.T) {
	a := New(t)
	ns := &networkServer{
		devices: device.NewRedisDeviceStore(GetRedisClient(), "ns-test-handle-downlink-adr"),
	}
	ns.InitStatus()

	defer func() {
		keys, _ := GetRedisClient().Keys("*ns-test-handle-downlink-adr*").Result()
		for _, key := range keys {
			GetRedisClient().Del(key).Result()
		}
	}()

	appEUI := types.AppEUI([8]byte{1})
	devEUI := types.DevEUI([8]byte{1})
	history, _ := ns.devices.Frames(appEUI, devEUI)
	dev := &device.Device{AppEUI: appEUI, DevEUI: devEUI}

	message := adrInitDownlinkMessage()

	var shouldReturnError = func() {
		a := New(t)
		message = adrInitDownlinkMessage()
		err := ns.handleDownlinkADR(message, dev)
		a.So(err, ShouldNotBeNil)
		a.So(message.Message.GetLorawan().GetMacPayload().FOpts, ShouldBeEmpty)
		if a.Failed() {
			_, file, line, _ := runtime.Caller(1)
			t.Errorf("\n%s:%d", file, line)
		}
	}
	var nothingShouldHappen = func() {
		a := New(t)
		message = adrInitDownlinkMessage()
		err := ns.handleDownlinkADR(message, dev)
		a.So(err, ShouldBeNil)
		a.So(message.Message.GetLorawan().GetMacPayload().FOpts, ShouldBeEmpty)
		if a.Failed() {
			_, file, line, _ := runtime.Caller(1)
			t.Errorf("\n%s:%d", file, line)
		}
	}

	// initially
	nothingShouldHappen()

	dev.ADR.SendReq = true
	nothingShouldHappen()

	var resetFrames = func(appEUI types.AppEUI, devEUI types.DevEUI) {
		history.Clear()
		for i := 0; i < 20; i++ {
			history.Push(&device.Frame{SNR: 10, GatewayCount: 3, FCnt: uint32(i)})
		}
	}
	resetFrames(dev.AppEUI, dev.DevEUI)

	nothingShouldHappen()

	dev.ADR.DataRate = "SF8BW125"
	nothingShouldHappen()

	dev.ADR.Band = "INVALID"
	shouldReturnError()

	dev.ADR.Band = "US_902_928"
	nothingShouldHappen()

	dev.ADR.Band = "EU_863_870"

	err := ns.handleDownlinkADR(message, dev)
	a.So(err, ShouldBeNil)
	fOpts := message.Message.GetLorawan().GetMacPayload().FOpts
	a.So(fOpts, ShouldHaveLength, 1)
	a.So(fOpts[0].Cid, ShouldEqual, lorawan.LinkADRReq)
	payload := new(lorawan.LinkADRReqPayload)
	payload.UnmarshalBinary(fOpts[0].Payload)
	a.So(payload.DataRate, ShouldEqual, 5) // SF7BW125
	a.So(payload.TXPower, ShouldEqual, 1)  // 14
	for i := 0; i < 8; i++ {               // First 8 channels enabled
		a.So(payload.ChMask[i], ShouldBeTrue)
	}
	a.So(payload.ChMask[8], ShouldBeFalse) // 9th channel (FSK) disabled

	shouldHaveNbTrans := func(nbTrans int) {
		a := New(t)
		message := adrInitDownlinkMessage()
		err := ns.handleDownlinkADR(message, dev)
		a.So(err, ShouldBeNil)
		fOpts := message.Message.GetLorawan().GetMacPayload().FOpts
		a.So(fOpts, ShouldHaveLength, 1)
		a.So(fOpts[0].Cid, ShouldEqual, lorawan.LinkADRReq)
		payload := new(lorawan.LinkADRReqPayload)
		payload.UnmarshalBinary(fOpts[0].Payload)
		a.So(payload.DataRate, ShouldEqual, 5) // SF7BW125
		a.So(payload.TXPower, ShouldEqual, 1)  // 14
		a.So(payload.Redundancy.NbRep, ShouldEqual, nbTrans)
		if a.Failed() {
			_, file, line, _ := runtime.Caller(1)
			t.Errorf("\n%s:%d", file, line)
		}
	}

	tests := map[int]map[int]int{
		1: map[int]int{0: 1, 1: 1, 2: 1, 4: 2, 10: 3},
		2: map[int]int{0: 1, 1: 1, 2: 2, 4: 3, 10: 3},
		3: map[int]int{0: 2, 1: 2, 2: 3, 4: 3, 10: 3},
	}

	for nbTrans, test := range tests {
		for loss, exp := range test {
			dev.ADR.NbTrans = nbTrans
			resetFrames(dev.AppEUI, dev.DevEUI)
			history.Push(&device.Frame{SNR: 10, GatewayCount: 3, FCnt: uint32(20 + loss)})
			if nbTrans == exp {
				nothingShouldHappen()
			} else {
				shouldHaveNbTrans(exp)
			}
		}
	}

	// Invalid case
	message = adrInitDownlinkMessage()
	dev.ADR.DataRate = "INVALID"
	shouldReturnError()

}
