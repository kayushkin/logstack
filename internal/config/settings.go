// Package config declares every environment variable logstack reads, once,
// with llm-bridge's servicesettings. The command reads its configuration from
// the registry; GET /settings describes the service from it; and a test holds
// every os.Getenv in the repo to it.
package config

import (
	"net/http"

	"github.com/kayushkin/llm-bridge/msg"
	"github.com/kayushkin/llm-bridge/servicesettings"
)

// ServiceName is this service's name in its own settings description.
const ServiceName = "logstack"

// OwnedEnvironmentVariablePrefix is the prefix of the variables that are this
// service's alone. A set variable carrying it that SettingDefinitions does not
// declare stops the service from starting. LOGSTACK_URL carries it too, and is
// how si and others find this service; declare it here if logstack ever runs
// with it set.
const OwnedEnvironmentVariablePrefix = "LOGSTACK_"

// Keys of the settings, as GET /settings names them.
const (
	SettingPort          = "port"
	SettingDataDirectory = "data_directory"
	SettingGinMode       = "gin_mode"
	SettingNATSURL       = "nats_url"
)

// SettingDefinitions declares every environment variable this process reads.
//
// Nothing here is Editable, and nothing may become so while the service has no
// operator gate: GET /settings is as open as every other route.
func SettingDefinitions() []servicesettings.Definition {
	return []servicesettings.Definition{
		{Key: SettingPort, EnvironmentVariable: "LOGSTACK_PORT", Kind: msg.ServiceSettingKindWiring, ValueType: msg.ServiceSettingValueTypeInteger, Default: "8081",
			Description: "The port the HTTP server listens on, on every interface. Changing it moves the service, so every LOGSTACK_URL that points at it must be changed too."},
		{Key: SettingDataDirectory, EnvironmentVariable: "LOGSTACK_DATA_DIR", Kind: msg.ServiceSettingKindPath, ValueType: msg.ServiceSettingValueTypeString, Default: "./logs",
			Description: "The directory of .jsonl log files, relative to the working directory unless absolute. Changing it serves and writes whatever logs are there; the old logs stay where they were."},
		{Key: SettingGinMode, EnvironmentVariable: "GIN_MODE", Kind: msg.ServiceSettingKindBehaviour, ValueType: msg.ServiceSettingValueTypeString, Default: "release",
			Description: "The gin framework's mode: release, debug or test. debug adds route and warning lines to the log; any other value stops the start."},
		{Key: SettingNATSURL, EnvironmentVariable: "NATS_URL", Kind: msg.ServiceSettingKindWiring, ValueType: msg.ServiceSettingValueTypeString, Default: "nats://localhost:4222",
			Description: "The NATS server logstack takes log entries and chat messages from, and answers logstack.query on. If it cannot connect, logstack serves HTTP only and logs a warning."},
	}
}

// NewSettingsRegistry reads this service's settings from environment. It fails
// on a value that does not parse and on a set LOGSTACK_ variable nobody
// declared.
func NewSettingsRegistry(environment servicesettings.Environment) (*servicesettings.Registry, error) {
	return servicesettings.New(ServiceName, []string{OwnedEnvironmentVariablePrefix}, SettingDefinitions(), environment)
}

// SettingsHandler serves the registry at GET /settings, the way every service
// serves its settings. PUT /settings/{key} is not mounted: no setting is
// Editable, so there is nothing a write could change.
func SettingsHandler(registry *servicesettings.Registry) http.Handler {
	return servicesettings.Handler(registry, "/settings")
}
