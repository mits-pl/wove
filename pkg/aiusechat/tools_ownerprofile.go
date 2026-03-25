// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
	"github.com/woveterm/wove/pkg/wavebase"
)

const OwnerProfileFilename = "owner.md"

func GetOwnerProfileToolDefinition() uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "get_owner_profile",
		DisplayName:      "Get Owner Profile",
		Description:      "Read the owner's personal profile (name, email, phone, address, payment preference) from the Wave config directory. Use this when you need the owner's details for checkout, form filling, or any task requiring personal information.",
		ShortDescription: "Read owner profile",
		ToolLogName:      "owner:getprofile",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":          map[string]any{},
			"required":            []any{},
			"additionalProperties": false,
		},
		Strict: true,
		ToolTextCallback: func(input any) (string, error) {
			configDir := wavebase.GetWaveConfigDir()
			profilePath := filepath.Join(configDir, OwnerProfileFilename)

			data, err := os.ReadFile(profilePath)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Sprintf(`Owner profile not found at: %s

You should ask the user for their details (name, email, phone, address, payment preference, delivery notes) and then create the file using write_text_file at the path above.

Use this format:
# Owner Profile

- **Name**: [full name]
- **Email**: [email]
- **Phone**: [phone]
- **Address**: [street, city, postal code]
- **Payment**: [BLIK / card / transfer]
- **Notes**: [delivery preferences, e.g. preferred parcel locker]
`, profilePath), nil
				}
				return "", fmt.Errorf("error reading owner profile: %w", err)
			}

			return string(data), nil
		},
	}
}
