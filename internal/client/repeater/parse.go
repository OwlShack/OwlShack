package repeater

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

type Neighbor struct {
	PubkeyPrefix string  `json:"pubkeyPrefix"`
	SecsAgo      uint32  `json:"secsAgo"`
	SNR          float64 `json:"snr"`
}

type Neighbors struct {
	TotalCount   int        `json:"totalCount"`
	ResultsCount int        `json:"resultsCount"`
	Neighbors    []Neighbor `json:"neighbors"`
}

type OwnerInfo struct {
	FirmwareVersion string `json:"firmwareVersion"`
	NodeName        string `json:"nodeName"`
	OwnerInfo       string `json:"ownerInfo"`
}

type AccessListEntry struct {
	PubkeyPrefix string `json:"pubkeyPrefix"`
	Permissions  uint8  `json:"permissions"`
}

type AccessList struct {
	Entries []AccessListEntry `json:"entries"`
}

type Status struct {
	BatteryMV   uint16  `json:"batteryMv"`
	QueueLen    uint16  `json:"queueLen"`
	NoiseFloor  int16   `json:"noiseFloor"`
	LastRSSI    int16   `json:"lastRssi"`
	PacketsRecv uint32  `json:"packetsRecv"`
	PacketsSent uint32  `json:"packetsSent"`
	TxAirSecs   uint32  `json:"txAirSecs"`
	RxAirSecs   uint32  `json:"rxAirSecs"`
	UptimeSecs  uint32  `json:"uptimeSecs"`
	FloodTx     uint32  `json:"floodTx"`
	DirectTx    uint32  `json:"directTx"`
	FloodRx     uint32  `json:"floodRx"`
	DirectRx    uint32  `json:"directRx"`
	ErrEvents   uint16  `json:"errEvents"`
	LastSNR     float64 `json:"lastSnr"`
	DirectDups  uint16  `json:"directDups"`
	FloodDups   uint16  `json:"floodDups"`
	RecvErrors  uint32  `json:"recvErrors"`
	ChanUtil    float64 `json:"chanUtil"`
	// Room servers only (ServerStats); nil on repeaters.
	Posted     *uint16 `json:"posted,omitempty"`
	PostPushes *uint16 `json:"postPushes,omitempty"`
}

func parseRepeaterNeighbors(data []byte, prefixLen int) (*Neighbors, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: neighbors data too short: got %d", ErrBadReply, len(data))
	}
	out := &Neighbors{
		TotalCount:   int(int16(binary.LittleEndian.Uint16(data[0:2]))),
		ResultsCount: int(int16(binary.LittleEndian.Uint16(data[2:4]))),
	}
	entrySize := prefixLen + 4 + 1
	pos := 4
	for i := 0; i < out.ResultsCount; i++ {
		if pos+entrySize > len(data) {
			break
		}
		secsAgo := binary.LittleEndian.Uint32(data[pos+prefixLen : pos+prefixLen+4])
		snr := float64(int8(data[pos+prefixLen+4])) / 4
		out.Neighbors = append(out.Neighbors, Neighbor{
			PubkeyPrefix: hex.EncodeToString(data[pos : pos+prefixLen]),
			SecsAgo:      secsAgo,
			SNR:          snr,
		})
		pos += entrySize
	}
	return out, nil
}

func parseRepeaterAccessList(data []byte) *AccessList {
	out := &AccessList{Entries: []AccessListEntry{}}
	const stride = 7 // 6-byte prefix + 1-byte permissions
	for pos := 0; pos+stride <= len(data); pos += stride {
		if data[pos+6] == 0 {
			continue // firmware skips deleted entries; this is cipher-block zero padding
		}
		out.Entries = append(out.Entries, AccessListEntry{
			PubkeyPrefix: hex.EncodeToString(data[pos : pos+6]),
			Permissions:  data[pos+6],
		})
	}
	return out
}

func parseRepeaterOwnerInfo(data []byte) *OwnerInfo {
	text := strings.TrimRight(string(data), "\x00")
	parts := strings.SplitN(text, "\n", 3)
	info := &OwnerInfo{}
	if len(parts) > 0 {
		info.FirmwareVersion = parts[0]
	}
	if len(parts) > 1 {
		info.NodeName = parts[1]
	}
	if len(parts) > 2 {
		info.OwnerInfo = parts[2]
	}
	return info
}

// parseRoomStatus: bytes 48-52 are n_posted and n_post_push (u16 each) where a repeater carries rx_air_time_secs.
func parseRoomStatus(data []byte) (*Status, error) {
	if len(data) < 52 {
		return nil, fmt.Errorf("%w: room status data too short: got %d, need 52", ErrBadReply, len(data))
	}
	s := parseCommonStats(data)
	posted := binary.LittleEndian.Uint16(data[48:50])
	pushes := binary.LittleEndian.Uint16(data[50:52])
	s.Posted, s.PostPushes = &posted, &pushes
	return s, nil
}

// parseCommonStats decodes the 48 bytes shared by RepeaterStats and ServerStats.
func parseCommonStats(data []byte) *Status {
	return &Status{
		BatteryMV:   binary.LittleEndian.Uint16(data[0:2]),
		QueueLen:    binary.LittleEndian.Uint16(data[2:4]),
		NoiseFloor:  int16(binary.LittleEndian.Uint16(data[4:6])),
		LastRSSI:    int16(binary.LittleEndian.Uint16(data[6:8])),
		PacketsRecv: binary.LittleEndian.Uint32(data[8:12]),
		PacketsSent: binary.LittleEndian.Uint32(data[12:16]),
		TxAirSecs:   binary.LittleEndian.Uint32(data[16:20]),
		UptimeSecs:  binary.LittleEndian.Uint32(data[20:24]),
		FloodTx:     binary.LittleEndian.Uint32(data[24:28]),
		DirectTx:    binary.LittleEndian.Uint32(data[28:32]),
		FloodRx:     binary.LittleEndian.Uint32(data[32:36]),
		DirectRx:    binary.LittleEndian.Uint32(data[36:40]),
		ErrEvents:   binary.LittleEndian.Uint16(data[40:42]),
		LastSNR:     float64(int16(binary.LittleEndian.Uint16(data[42:44]))) / 4.0,
		DirectDups:  binary.LittleEndian.Uint16(data[44:46]),
		FloodDups:   binary.LittleEndian.Uint16(data[46:48]),
	}
}

func parseRepeaterStatus(data []byte) (*Status, error) {
	if len(data) < 52 {
		return nil, fmt.Errorf("%w: status data too short: got %d, need at least 52", ErrBadReply, len(data))
	}

	s := &Status{
		BatteryMV:   binary.LittleEndian.Uint16(data[0:2]),
		QueueLen:    binary.LittleEndian.Uint16(data[2:4]),
		NoiseFloor:  int16(binary.LittleEndian.Uint16(data[4:6])),
		LastRSSI:    int16(binary.LittleEndian.Uint16(data[6:8])),
		PacketsRecv: binary.LittleEndian.Uint32(data[8:12]),
		PacketsSent: binary.LittleEndian.Uint32(data[12:16]),
		TxAirSecs:   binary.LittleEndian.Uint32(data[16:20]),
		UptimeSecs:  binary.LittleEndian.Uint32(data[20:24]),
		FloodTx:     binary.LittleEndian.Uint32(data[24:28]),
		DirectTx:    binary.LittleEndian.Uint32(data[28:32]),
		FloodRx:     binary.LittleEndian.Uint32(data[32:36]),
		DirectRx:    binary.LittleEndian.Uint32(data[36:40]),
		ErrEvents:   binary.LittleEndian.Uint16(data[40:42]),
		LastSNR:     float64(int16(binary.LittleEndian.Uint16(data[42:44]))) / 4.0,
		DirectDups:  binary.LittleEndian.Uint16(data[44:46]),
		FloodDups:   binary.LittleEndian.Uint16(data[46:48]),
	}

	if len(data) >= 52 {
		rxAirSecs := binary.LittleEndian.Uint32(data[48:52])
		s.RxAirSecs = rxAirSecs
		if s.UptimeSecs > 0 {
			s.ChanUtil = float64(s.TxAirSecs+rxAirSecs) / float64(s.UptimeSecs) * 100
		}
	}
	if len(data) >= 56 {
		s.RecvErrors = binary.LittleEndian.Uint32(data[52:56])
	}

	return s, nil
}
