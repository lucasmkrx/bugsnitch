// SPDX-License-Identifier: MPL-2.0
package agent

import "fmt"

// Usage retains only provider-supplied allowance windows. An absent field is
// unknown, including whether ordinary work can resume.
type Usage struct {
	OrdinaryUsageAllowed *bool                    `json:"ordinaryUsageAllowed"`
	RateLimits           LimitSnapshot            `json:"rateLimits"`
	RateLimitsByLimitID  map[string]LimitSnapshot `json:"rateLimitsByLimitId"`
}
type LimitSnapshot struct {
	Primary   *UsageWindow `json:"primary"`
	Secondary *UsageWindow `json:"secondary"`
}
type UsageWindow struct {
	UsedPercent           *int   `json:"usedPercent"`
	WindowDurationMinutes *int64 `json:"windowDurationMins"`
	ResetsAt              *int64 `json:"resetsAt"`
}

func (c *Client) Usage() (Usage, error) {
	var u Usage
	if err := c.call("account/rateLimits/read", nil, &u); err != nil {
		return u, fmt.Errorf("account usage unknown: %w", err)
	}
	return u, nil
}
