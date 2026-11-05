// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package detect

import (
	"strings"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
)

// key reads the value of the first assignment to rule.Key in a file of
// key=value lines, such as .sdkmanrc or gradle.properties. Blank lines
// and lines starting with "#" or "!" are comments; the separator is
// "=" or ":", and whitespace around key and value is ignored. Line
// continuations and escapes are not interpreted.
func (p *project) key(rule catalog.DetectRule) (*finding, error) {
	data, exists, err := p.read(rule.File)
	if err != nil || !exists {
		return nil, err
	}
	for raw := range strings.SplitSeq(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || line[0] == '!' {
			continue
		}
		i := strings.IndexAny(line, "=:")
		if i < 0 || strings.TrimSpace(line[:i]) != rule.Key {
			continue
		}
		v := strings.TrimSpace(line[i+1:])
		if v == "" {
			return &finding{empty: true}, nil
		}
		return &finding{text: v}, nil
	}
	return nil, nil
}
