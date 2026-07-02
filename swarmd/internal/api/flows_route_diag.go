package api

import (
	"fmt"
	"log"
	"strings"

	"swarm/packages/swarmd/internal/flowdiaglog"
)

func flowRouteDiagLog(stage string, fields ...any) {
	stage = strings.TrimSpace(stage)
	if stage == "" {
		stage = "unknown"
	}
	parts := make([]string, 0, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		key := strings.TrimSpace(fmt.Sprint(fields[i]))
		if key == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", key, strings.TrimSpace(fmt.Sprint(fields[i+1]))))
	}
	message := fmt.Sprintf("flow_route_diag stage=%q %s", stage, strings.Join(parts, " "))
	log.Print(message)
	flowdiaglog.Append(message)
}
