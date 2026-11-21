// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

func rotate() error {
	p, err := getProfile()
	if err != nil {
		return err
	}
	if p.Enrollment.DeviceID == "" {
		return errors.New("complete pairing before rotation")
	}
	if p.NextCredential == "" {
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return err
		}
		p.NextCredential = hex.EncodeToString(key)
		raw, _ := json.Marshal(p)
		n, e := journal("UPDATE profile SET data=" + sqlValue(string(raw)) + " WHERE id=1 AND COALESCE(json_extract(data,'$.NextCredential'),'')='' AND json_extract(data,'$.Credential')=" + sqlValue(p.Credential) + ";SELECT changes();")
		if e != nil {
			return e
		}
		if n != "1" {
			return errors.New("another rotation is pending; retry rotate")
		}
	}
	var ack struct{ DeviceID string }
	err = send(context.Background(), p, "rotate", map[string]any{"protocol": 1, "credential": p.NextCredential}, &ack)
	if err != nil {
		return err
	}
	if ack.DeviceID != p.Enrollment.DeviceID {
		return errors.New("device identity changed")
	}
	old := p.Credential
	p.Credential = p.NextCredential
	p.NextCredential = ""
	raw, _ := json.Marshal(p)
	_, err = journal("UPDATE profile SET data=" + sqlValue(string(raw)) + " WHERE id=1 AND json_extract(data,'$.Credential')=" + sqlValue(old) + " AND json_extract(data,'$.NextCredential')=" + sqlValue(p.Credential) + ";")
	if err == nil {
		fmt.Println("Rotated credential for", ack.DeviceID)
	}
	return err
}
