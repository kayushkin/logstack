package config

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kayushkin/llm-bridge/msg"
	"github.com/kayushkin/llm-bridge/servicesettings"
)

// repositoryRoot is where the source scan starts: this package is
// internal/config.
const repositoryRoot = "../.."

// The registry gives the command what its getEnv reads gave it before
// 2026-09-24: the same defaults with nothing set, and the operator's values
// when they are.
func TestTheRegistryReadsTheSameValuesTheCommandAlwaysDid(t *testing.T) {
	unset, err := NewSettingsRegistry(servicesettings.MapEnvironment(map[string]string{"HOME": "/home/someone"}))
	if err != nil {
		t.Fatal(err)
	}
	if unset.Integer(SettingPort) != 8081 || unset.String(SettingDataDirectory) != "./logs" ||
		unset.String(SettingGinMode) != "release" || unset.String(SettingNATSURL) != "nats://localhost:4222" {
		t.Errorf("defaults changed: %+v", unset.Describe())
	}

	set, err := NewSettingsRegistry(servicesettings.MapEnvironment(map[string]string{
		"LOGSTACK_PORT":     "8088",
		"LOGSTACK_DATA_DIR": "/srv/logs",
		"GIN_MODE":          "debug",
		"NATS_URL":          "nats://bus:4222",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if set.Integer(SettingPort) != 8088 || set.String(SettingDataDirectory) != "/srv/logs" ||
		set.String(SettingGinMode) != "debug" || set.String(SettingNATSURL) != "nats://bus:4222" {
		t.Errorf("set values not read back: %+v", set.Describe())
	}

	// A variable set to the empty string is the same as unset, as it was when
	// getEnv compared os.Getenv to "".
	empty, err := NewSettingsRegistry(servicesettings.MapEnvironment(map[string]string{"LOGSTACK_PORT": "", "GIN_MODE": ""}))
	if err != nil {
		t.Fatal(err)
	}
	if empty.Integer(SettingPort) != 8081 || empty.String(SettingGinMode) != "release" {
		t.Errorf("empty variables: port=%d gin mode=%q", empty.Integer(SettingPort), empty.String(SettingGinMode))
	}
}

func TestTheRegistryRefusesALogstackVariableNobodyDeclaredAndAPortThatIsNotANumber(t *testing.T) {
	_, err := NewSettingsRegistry(servicesettings.MapEnvironment(map[string]string{"LOGSTACK_DATADIR": "/x"}))
	if err == nil || !strings.Contains(err.Error(), "LOGSTACK_DATADIR is set and logstack declares no such setting") {
		t.Fatalf("NewSettingsRegistry = %v, want a refusal naming the misspelled variable", err)
	}
	_, err = NewSettingsRegistry(servicesettings.MapEnvironment(map[string]string{"LOGSTACK_PORT": "eighty"}))
	if err == nil || !strings.Contains(err.Error(), "LOGSTACK_PORT") {
		t.Fatalf("NewSettingsRegistry = %v, want a refusal naming the port", err)
	}
	if _, err := NewSettingsRegistry(servicesettings.MapEnvironment(map[string]string{"LOGSTACK_PORT": "1", "PATH": "/bin", "HOME": "/root"})); err != nil {
		t.Errorf("a declared variable and two outside the prefix were refused: %v", err)
	}
}

func TestGetSettingsDescribesTheServiceAndNothingCanBeWritten(t *testing.T) {
	registry, err := NewSettingsRegistry(servicesettings.MapEnvironment(map[string]string{"LOGSTACK_PORT": "9999"}))
	if err != nil {
		t.Fatal(err)
	}
	handler := SettingsHandler(registry)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d: %s", recorder.Code, recorder.Body)
	}
	var described msg.ServiceSettings
	if err := json.Unmarshal(recorder.Body.Bytes(), &described); err != nil {
		t.Fatal(err)
	}
	if described.Service != ServiceName || len(described.Settings) != len(SettingDefinitions()) {
		t.Fatalf("service=%q with %d settings, want %q with %d", described.Service, len(described.Settings), ServiceName, len(SettingDefinitions()))
	}
	for _, setting := range described.Settings {
		if setting.Editable {
			t.Errorf("%s is editable, and this service has no operator gate to put a write behind", setting.Key)
		}
		if setting.Key == SettingPort && (setting.Value != "9999" || setting.Source != msg.ServiceSettingSourceEnvironment) {
			t.Errorf("port served as %q from %q", setting.Value, setting.Source)
		}
	}
}

// Every environment variable the service's own code reads by name is declared.
// A read that is not declared is invisible on the settings page and escapes the
// startup check. The walk is scheduler's environmentReadFaults, as dash copied it.
func TestEveryEnvironmentVariableTheServiceReadsIsDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, definition := range SettingDefinitions() {
		declared[definition.EnvironmentVariable] = true
	}

	filesRead := 0
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == "node_modules" || entry.Name() == ".git") {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		filesRead++
		for _, fault := range environmentReadFaults(file, declared) {
			t.Errorf("%s %s", path, fault)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// If this package moved, the walk would start somewhere else, read nothing
	// and pass.
	if _, err := os.Stat(filepath.Join(repositoryRoot, "cmd", "logstack", "main.go")); err != nil {
		t.Fatalf("the scan starts somewhere that is not the repository root: %v", err)
	}
	if filesRead < 8 {
		t.Fatalf("the scan read %d files; it is not looking at the service", filesRead)
	}
}

// The scan's own controls: each shape it exists to refuse is refused.
func TestTheSourceScanRefusesEachShapeOfUndeclaredRead(t *testing.T) {
	declared := map[string]bool{"LOGSTACK_PORT": true}
	for name, source := range map[string]string{
		"an undeclared name":      `package p; import "os"; var v = os.Getenv("LOGSTACK_PORTS")`,
		"a computed name":         `package p; import "os"; var n = "X"; var v = os.Getenv(n)`,
		"the whole environment":   `package p; import "os"; var v = os.Environ()`,
		"os.Getenv as a value":    `package p; import "os"; var read = os.Getenv`,
		"an undeclared LookupEnv": `package p; import "os"; func f() { os.LookupEnv("OTHER") }`,
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "control.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if faults := environmentReadFaults(file, declared); len(faults) == 0 {
			t.Errorf("%s: the scan found nothing", name)
		}
	}
	file, err := parser.ParseFile(token.NewFileSet(), "control.go", `package p; import "os"; var v = os.Getenv("LOGSTACK_PORT")`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if faults := environmentReadFaults(file, declared); len(faults) != 0 {
		t.Errorf("a declared read was refused: %v", faults)
	}
}

func environmentReadFaults(file *ast.File, declared map[string]bool) []string {
	var faults []string
	called := map[*ast.SelectorExpr]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, isCall := node.(*ast.CallExpr)
		if !isCall {
			return true
		}
		selector, isSelector := call.Fun.(*ast.SelectorExpr)
		if !isSelector || !isOsFunction(selector, "Getenv", "LookupEnv") {
			return true
		}
		called[selector] = true
		literal, isLiteral := call.Args[0].(*ast.BasicLit)
		if !isLiteral {
			faults = append(faults, "reads an environment variable whose name is computed, which no declaration can be held to")
			return true
		}
		name, _ := strconv.Unquote(literal.Value)
		if !declared[name] {
			faults = append(faults, "reads "+name+", which SettingDefinitions does not declare")
		}
		return true
	})
	ast.Inspect(file, func(node ast.Node) bool {
		selector, isSelector := node.(*ast.SelectorExpr)
		if !isSelector {
			return true
		}
		if isOsFunction(selector, "Environ") {
			faults = append(faults, "reads the whole environment, which no declaration can be held to")
		}
		if isOsFunction(selector, "Getenv", "LookupEnv") && !called[selector] {
			faults = append(faults, "hands os."+selector.Sel.Name+" on as a value, so the names it reads cannot be seen here")
		}
		return true
	})
	return faults
}

func isOsFunction(selector *ast.SelectorExpr, names ...string) bool {
	packageName, isIdentifier := selector.X.(*ast.Ident)
	if !isIdentifier || packageName.Name != "os" {
		return false
	}
	for _, name := range names {
		if selector.Sel.Name == name {
			return true
		}
	}
	return false
}
