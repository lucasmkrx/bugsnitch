// SPDX-License-Identifier: MPL-2.0
package protocol

type TurnMeter struct {
	Subject  string `json:"subject"`
	Sequence int64  `json:"sequence"`
	General  bool   `json:"general,omitempty"`
	Model    string `json:"model"`
	Input    int64  `json:"input"`
	Output   int64  `json:"output"`
}
