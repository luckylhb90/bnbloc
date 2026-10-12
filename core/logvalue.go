package core

import (
	"log/slog"
	"reflect"
	"sort"
)

// Log redaction is one rule for every secret-bearing type of the core seam,
// the Core Protocol and the Sandbox Protocol: a type with a secret field
// implements slog.LogValuer as LogRedacted(x) (core.AgentProfile keeps its
// own summary: skill and server names), and TestSecretsRedacted fails on a
// type that carries a secret without one (or prints a secret).

// secretLogFields are logged as "[redacted]": bearer tokens, exec tokens and
// the value half of an env variable, header or secret binding.
var secretLogFields = map[string]bool{"Token": true, "ExecToken": true, "Value": true}

// namedLogFields map names to secret values (environment, headers): only the
// names are logged.
var namedLogFields = map[string]bool{"Env": true, "Headers": true}

// contentLogFields are user content (prompts, instructions, file bodies):
// only their size is logged.
var contentLogFields = map[string]bool{
	"Prompt": true, "Text": true, "Instructions": true, "Content": true,
	"ContentBase64": true, "StdinBase64": true,
}

// SecretLogField reports whether a struct field of this name carries a
// secret wherever it appears (a token, an env variable or header value).
func SecretLogField(name string) bool { return secretLogFields[name] || namedLogFields[name] }

// LogRedacted renders a struct (or a pointer to one) for slog without its
// secrets: secret fields as "[redacted]", env and headers as their names,
// content as its size, nested values through their own LogValue (or this
// rule), and lists of records as their length. Zero fields are omitted.
func LogRedacted(v any) slog.Value {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return slog.StringValue("<nil>")
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return slog.StringValue("[redacted]")
	}
	t := rv.Type()
	attrs := make([]slog.Attr, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fv := rv.Field(i)
		if !f.IsExported() || fv.IsZero() {
			continue
		}
		switch {
		case secretLogFields[f.Name]:
			attrs = append(attrs, slog.String(f.Name, "[redacted]"))
		case namedLogFields[f.Name]:
			attrs = append(attrs, slog.Any(f.Name, logNames(fv)))
		case contentLogFields[f.Name] && (fv.Kind() == reflect.String || fv.Kind() == reflect.Slice):
			attrs = append(attrs, slog.Int(f.Name+"Bytes", fv.Len()))
		default:
			attrs = append(attrs, slog.Attr{Key: f.Name, Value: logField(fv)})
		}
	}
	return slog.GroupValue(attrs...)
}

// logField renders one non-secret field value.
func logField(fv reflect.Value) slog.Value {
	if fv.Kind() == reflect.Pointer && fv.IsNil() {
		return slog.StringValue("<nil>")
	}
	if lv, ok := fv.Interface().(slog.LogValuer); ok {
		return lv.LogValue()
	}
	switch fv.Kind() {
	case reflect.Pointer, reflect.Struct:
		return LogRedacted(fv.Interface())
	case reflect.Slice, reflect.Array, reflect.Map:
		if record(fv.Type().Elem()) {
			return slog.IntValue(fv.Len())
		}
	}
	return slog.AnyValue(fv.Interface())
}

// record reports a struct (or pointer to struct) element type: a list of
// records is logged as its length.
func record(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct
}

// logNames returns the names of an env map or a name/value list, sorted.
func logNames(fv reflect.Value) []string {
	var names []string
	switch fv.Kind() {
	case reflect.Map:
		for _, k := range fv.MapKeys() {
			if k.Kind() == reflect.String {
				names = append(names, k.String())
			}
		}
	case reflect.Slice:
		for i := 0; i < fv.Len(); i++ {
			el := fv.Index(i)
			for el.Kind() == reflect.Pointer && !el.IsNil() {
				el = el.Elem()
			}
			if el.Kind() == reflect.Struct {
				if n := el.FieldByName("Name"); n.IsValid() && n.Kind() == reflect.String {
					names = append(names, n.String())
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// LogValue keeps the endpoint's bearer out of every log line.
func (e Endpoint) LogValue() slog.Value { return LogRedacted(e) }

// LogValue keeps the exec token out of every log line.
func (s SandboxExecution) LogValue() slog.Value { return LogRedacted(s) }

// LogValue keeps the sandbox exec token out of every log line.
func (e Execution) LogValue() slog.Value { return LogRedacted(e) }

// LogValue keeps the execution's exec token out of every log line.
func (o SessionOptions) LogValue() slog.Value { return LogRedacted(o) }

// LogValue keeps the profile's secrets and the execution token out of every
// log line.
func (r SessionRequest) LogValue() slog.Value { return LogRedacted(r) }

// LogValue keeps MCP env and header values out of every log line.
func (m MCPServer) LogValue() slog.Value { return LogRedacted(m) }

// LogValue keeps the value (an env or header secret) out of every log line.
func (nv NameValue) LogValue() slog.Value { return LogRedacted(nv) }
