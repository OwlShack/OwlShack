// Package meshpath builds the PATH packets that teach a peer its route back to us. Both the
// companion (DM path returns) and the repeater client (reciprocal returns) need them.
package meshpath

import (
	"crypto/rand"
	"fmt"

	meshcore "github.com/OwlShack/meshcore-go"
)

// BuildReturn matches the firmware's createPathReturn:
// [dest_hash:1][src_hash:1][MAC:2][encrypted([pathLenByte][path_data][extra_type][extra_data])].
// With no extra it appends 0xFF plus 4 random bytes (Mesh.cpp:472-477): AES-ECB has no IV, so two
// returns over the same route would otherwise encrypt identically and relays drop the second.
func BuildReturn(selfPubKey [meshcore.PubKeySize]byte, destPubKey, sharedSecret, inPath []byte, pathLenByte, extraType byte, extraData []byte) (*meshcore.Packet, error) {
	pathHashSize := int((pathLenByte>>6)&3) + 1
	pathDataLen := int(pathLenByte&63) * pathHashSize
	if pathDataLen > len(inPath) {
		pathDataLen = len(inPath)
	}

	plain := make([]byte, 0, 1+pathDataLen+5+len(extraData))
	plain = append(plain, pathLenByte)
	plain = append(plain, inPath[:pathDataLen]...)
	if len(extraData) == 0 {
		var salt [4]byte
		if _, err := rand.Read(salt[:]); err != nil {
			return nil, fmt.Errorf("salting path return: %w", err)
		}
		plain = append(plain, 0xFF)
		plain = append(plain, salt[:]...)
	} else {
		plain = append(plain, extraType)
		plain = append(plain, extraData...)
	}

	encrypted, err := meshcore.EncryptThenMAC(sharedSecret, plain)
	if err != nil {
		return nil, fmt.Errorf("encrypting path return: %w", err)
	}

	payload := make([]byte, 0, 2*meshcore.PathHashSize+len(encrypted))
	payload = append(payload, destPubKey[:meshcore.PathHashSize]...)
	payload = append(payload, selfPubKey[:meshcore.PathHashSize]...)
	payload = append(payload, encrypted...)

	return &meshcore.Packet{
		Header:     meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypePath, 0),
		PathLength: meshcore.MakePathLen(meshcore.PathHashSize, 0),
		Payload:    payload,
	}, nil
}

// Direct re-points a return packet down a known route, as the firmware's sendDirect does.
func Direct(pkt *meshcore.Packet, path []byte, hashSize uint8) {
	if hashSize == 0 {
		hashSize = 1
	}
	pkt.Header = meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypePath, 0)
	pkt.Path = path
	pkt.PathLength = meshcore.MakePathLen(hashSize, uint8(len(path)/int(hashSize)))
}
