package protocol

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
)

// types.go is the Core Protocol wire contract: every field of every struct
// it declares carries an explicit camelCase json tag (an untagged field would
// silently serialize PascalCase). Types that are not wire bodies live in
// other files.
func TestWireTypesEveryFieldTagged(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "types.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	structs := 0
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		structs++
		for _, field := range st.Fields.List {
			names := []string{"(embedded)"}
			if len(field.Names) > 0 {
				names = names[:0]
				for _, n := range field.Names {
					names = append(names, n.Name)
				}
			}
			tag := ""
			if field.Tag != nil {
				tag = reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Get("json")
			}
			name := strings.Split(tag, ",")[0]
			if name == "" || name == "-" {
				t.Errorf("%s.%s has no json tag", ts.Name.Name, strings.Join(names, ","))
				continue
			}
			if c := name[0]; c < 'a' || c > 'z' {
				t.Errorf("%s.%s json tag %q is not camelCase", ts.Name.Name, strings.Join(names, ","), name)
			}
		}
		return true
	})
	if structs < 20 {
		t.Fatalf("found %d structs in types.go; the scan is broken", structs)
	}
}

// fillAll sets every exported field of v to a non-zero value, so a mapper
// that drops a field (or a field added to one side only) fails the round
// trip.
func fillAll(v reflect.Value, seed *int) {
	*seed++
	switch v.Kind() {
	case reflect.String:
		v.SetString("v" + strings.Repeat("x", *seed%7) + string(rune('a'+*seed%26)))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(*seed))
	case reflect.Uint8:
		v.SetUint(uint64('a' + *seed%26))
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillAll(v.Elem(), seed)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillAll(v.Field(i), seed)
			}
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 2, 2)
		fillAll(s.Index(0), seed)
		fillAll(s.Index(1), seed)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fillAll(k, seed)
		val := reflect.New(v.Type().Elem()).Elem()
		if val.Kind() == reflect.Interface {
			val.Set(reflect.ValueOf("metric"))
		} else {
			fillAll(val, seed)
		}
		m.SetMapIndex(k, val)
		v.Set(m)
	}
}

func filled[T any](t *testing.T) T {
	t.Helper()
	var v T
	seed := 0
	fillAll(reflect.ValueOf(&v).Elem(), &seed)
	return v
}

// throughJSON encodes a wire value and decodes it back, as it crosses the
// wire.
func throughJSON[T any](t *testing.T, v T) T {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var back T
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	return back
}

// Every core <-> wire mapper pair is lossless for a value with every field
// set, across a JSON encode/decode: a field one side gains without the
// mapper, or a mapper that drops one, fails here.
func TestWireMappersRoundTrip(t *testing.T) {
	check := func(name string, want, got any) {
		t.Helper()
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s does not round-trip:\n want %+v\n  got %+v", name, want, got)
		}
	}

	req := filled[core.SessionRequest](t)
	req.Profile = validProfile(req.Profile)
	back, err := throughJSON(t, WireCreateSession(req)).Core(req.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	check("SessionRequest", req, back)

	opts := filled[core.SessionOptions](t)
	in := throughJSON(t, SendInputRequest{Text: "next", Options: WireOptions(opts), Execution: WireExecution(opts.Execution)})
	gotOpts, err := in.CoreOptions()
	if err != nil {
		t.Fatal(err)
	}
	check("SessionOptions", opts, gotOpts)

	res := filled[core.SessionResult](t)
	check("SessionResult", res, throughJSON(t, WireSessionResult(res)).Core())

	targets := filled[[]core.Target](t)
	check("Target", targets, throughJSON(t, ListTargetsResponse{Targets: WireTargets(targets)}).Core())

	msgs := filled[[]core.Message](t)
	check("Message", msgs, throughJSON(t, MessagesResponse{Messages: WireMessages(msgs)}).Core())

	turn := filled[core.TurnStatus](t)
	check("TurnStatus", turn, throughJSON(t, WireTurnStatus(turn)).Core())

	files := filled[[]core.SessionFile](t)
	check("SessionFile", files, throughJSON(t, ListSessionFilesResponse{Files: WireSessionFiles(files)}).Core())
}

// validProfile keeps a filled profile's every field set but makes it pass
// AgentProfile.Core's validation (names, paths, MCP transports).
func validProfile(p *core.AgentProfile) *core.AgentProfile {
	p.Env = map[string]string{"API_KEY": "secret"}
	for i := range p.Skills {
		p.Skills[i].Name = []string{"review", "lint"}[i%2]
		for j := range p.Skills[i].Files {
			p.Skills[i].Files[j].Path = []string{"SKILL.md", "scripts/run.sh"}[j%2]
		}
	}
	p.Skills[1].Files[0].Content = []byte{0xff, 0x00, 0xfe} // binary: travels as contentBase64
	for i := range p.MCPServers {
		p.MCPServers[i].Name = []string{"room", "fs"}[i%2]
		p.MCPServers[i].Type = []string{core.MCPHTTP, core.MCPSSE}[i%2]
	}
	return p
}
