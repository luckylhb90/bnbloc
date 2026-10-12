package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// emitter renders one Go package's exported declarations as TypeScript.
type emitter struct {
	fset  *token.FileSet
	pkg   *types.Package
	info  *types.Info
	files []*ast.File
	// foreign are TS type names defined outside the package (the external
	// enums) that a `tstype` tag may name.
	foreign map[string]bool
	// declared are the TS type names this package defines.
	declared map[string]bool
	// unions collects `//wire:union` members, in order of appearance.
	unions     map[string][]string
	unionOrder []string
	b          bytes.Buffer
}

// emitPackage type-checks the package in dir and renders it. foreign names
// the TS types defined elsewhere in the output that tstype tags may use.
func emitPackage(dir, importPath string, foreign []string) ([]byte, error) {
	fset := token.NewFileSet()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check(importPath, fset, files, info)
	if err != nil {
		return nil, err
	}
	e := &emitter{fset: fset, pkg: pkg, info: info, files: files, foreign: map[string]bool{}, declared: map[string]bool{}, unions: map[string][]string{}}
	for _, n := range foreign {
		e.foreign[n] = true
	}
	for _, name := range pkg.Scope().Names() {
		if tn, ok := pkg.Scope().Lookup(name).(*types.TypeName); ok && tn.Exported() {
			e.declared[name] = true
		}
	}
	// Union names are known before any field names one.
	for _, f := range files {
		for _, decl := range f.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.CONST {
				if u := directive(gd.Doc); u != "" {
					e.foreign[u] = true
				}
				for _, spec := range gd.Specs {
					if u := directive(spec.(*ast.ValueSpec).Doc); u != "" {
						e.foreign[u] = true
					}
				}
			}
		}
	}
	for _, f := range files {
		if err := e.file(f); err != nil {
			return nil, err
		}
	}
	if err := e.renderUnions(); err != nil {
		return nil, err
	}
	return e.b.Bytes(), nil
}

func (e *emitter) file(f *ast.File) error {
	header := false
	section := func() {
		if !header {
			name := filepath.Base(e.fset.Position(f.Package).Filename)
			fmt.Fprintf(&e.b, "\n// ---- %s ----\n", filepath.ToSlash(filepath.Join(e.pkg.Name(), name)))
			header = true
		}
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		switch gd.Tok {
		case token.TYPE:
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				if !ts.Name.IsExported() {
					continue
				}
				section()
				e.b.WriteString("\n")
				doc := ts.Doc
				if doc == nil && len(gd.Specs) == 1 {
					doc = gd.Doc
				}
				if err := e.typeDecl(ts, doc); err != nil {
					return err
				}
			}
		case token.CONST:
			blockUnion := directive(gd.Doc)
			wroteBlockDoc := false
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				union := directive(vs.Doc)
				if union == "" {
					union = blockUnion
				}
				for _, name := range vs.Names {
					if !name.IsExported() {
						continue
					}
					c := e.info.Defs[name].(*types.Const)
					lit, err := constLiteral(c.Val())
					if err != nil {
						return fmt.Errorf("const %s: %w", name.Name, err)
					}
					section()
					if !wroteBlockDoc {
						e.b.WriteString("\n")
					}
					if !wroteBlockDoc && len(gd.Specs) > 1 {
						comment(&e.b, "", gd.Doc)
					}
					wroteBlockDoc = true
					d := vs.Doc
					if d == nil && len(gd.Specs) == 1 {
						d = gd.Doc
					}
					comment(&e.b, "", d)
					fmt.Fprintf(&e.b, "export const %s = %s;\n", name.Name, lit)
					if union != "" {
						if _, ok := e.unions[union]; !ok {
							e.unionOrder = append(e.unionOrder, union)
						}
						e.unions[union] = append(e.unions[union], lit)
					}
				}
			}
		case token.VAR:
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !name.IsExported() || i >= len(vs.Values) {
						continue
					}
					lit, ok := e.literal(vs.Values[i], 0)
					if !ok {
						continue // computed at runtime: not part of the contract
					}
					v := e.info.Defs[name].(*types.Var)
					ts, err := e.tsType(v.Type(), "")
					if err != nil {
						return fmt.Errorf("var %s: %w", name.Name, err)
					}
					section()
					e.b.WriteString("\n")
					d := vs.Doc
					if d == nil && len(gd.Specs) == 1 {
						d = gd.Doc
					}
					comment(&e.b, "", d)
					fmt.Fprintf(&e.b, "export const %s: %s = %s;\n", name.Name, readonly(ts), lit)
				}
			}
		}
	}
	return nil
}

// typeDecl renders one exported type.
func (e *emitter) typeDecl(ts *ast.TypeSpec, doc *ast.CommentGroup) error {
	if ts.TypeParams != nil {
		return fmt.Errorf("type %s: generic types have no JSON shape", ts.Name.Name)
	}
	tn := e.info.Defs[ts.Name].(*types.TypeName)
	comment(&e.b, "", doc)
	if st, ok := ts.Type.(*ast.StructType); ok && !ts.Assign.IsValid() {
		return e.structDecl(ts.Name.Name, st)
	}
	if basic, ok := tn.Type().Underlying().(*types.Basic); ok && !ts.Assign.IsValid() {
		if vals := e.constsOf(tn); len(vals) > 0 {
			fmt.Fprintf(&e.b, "export type %s = %s;\n", ts.Name.Name, strings.Join(vals, " | "))
			return nil
		}
		fmt.Fprintf(&e.b, "export type %s = %s;\n", ts.Name.Name, basicTS(basic))
		return nil
	}
	under := tn.Type().Underlying()
	if ts.Assign.IsValid() {
		under = tn.Type()
	}
	t, err := e.tsType(under, "")
	if err != nil {
		return fmt.Errorf("type %s: %w", ts.Name.Name, err)
	}
	fmt.Fprintf(&e.b, "export type %s = %s;\n", ts.Name.Name, t)
	return nil
}

// constsOf lists the values of every package constant of the named type, in
// source order (the union a named string type stands for).
func (e *emitter) constsOf(tn *types.TypeName) []string {
	type entry struct {
		pos token.Pos
		lit string
	}
	var out []entry
	for _, name := range e.pkg.Scope().Names() {
		c, ok := e.pkg.Scope().Lookup(name).(*types.Const)
		if !ok || c.Type() != tn.Type() {
			continue
		}
		lit, err := constLiteral(c.Val())
		if err != nil {
			continue
		}
		out = append(out, entry{c.Pos(), lit})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pos < out[j].pos })
	vals := make([]string, 0, len(out))
	for _, v := range out {
		vals = append(vals, v.lit)
	}
	return vals
}

func (e *emitter) structDecl(name string, st *ast.StructType) error {
	var extends []string
	var body bytes.Buffer
	for _, f := range st.Fields.List {
		tag := ""
		if f.Tag != nil {
			unq, err := strconv.Unquote(f.Tag.Value)
			if err != nil {
				return err
			}
			tag = unq
		}
		jsonName, opts := parseJSONTag(reflect.StructTag(tag).Get("json"))
		if jsonName == "-" && opts == "" {
			continue
		}
		optional := hasOpt(opts, "omitempty") || hasOpt(opts, "omitzero")
		override := reflect.StructTag(tag).Get("tstype")
		if len(f.Names) == 0 { // embedded
			t := e.info.Types[f.Type].Type
			if jsonName == "" {
				named := namedOf(t)
				if named == nil || named.Obj().Pkg() != e.pkg {
					return fmt.Errorf("%s: embedded %s must be a struct of this package", name, t)
				}
				extends = append(extends, named.Obj().Name())
				continue
			}
			if err := e.field(&body, f, jsonName, optional, override, t, opts); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			continue
		}
		for _, n := range f.Names {
			if !n.IsExported() {
				continue
			}
			jn := jsonName
			if jn == "" {
				jn = n.Name
			}
			v := e.info.Defs[n].(*types.Var)
			if err := e.field(&body, f, jn, optional, override, v.Type(), opts); err != nil {
				return fmt.Errorf("%s.%s: %w", name, n.Name, err)
			}
		}
	}
	ext := ""
	if len(extends) > 0 {
		ext = " extends " + strings.Join(extends, ", ")
	}
	if body.Len() == 0 {
		fmt.Fprintf(&e.b, "export interface %s%s {}\n", name, ext)
		return nil
	}
	fmt.Fprintf(&e.b, "export interface %s%s {\n", name, ext)
	e.b.Write(body.Bytes())
	e.b.WriteString("}\n")
	return nil
}

func (e *emitter) field(b *bytes.Buffer, f *ast.Field, jsonName string, optional bool, override string, t types.Type, opts string) error {
	var ts string
	switch {
	case override != "":
		if !e.declared[override] && !e.foreign[override] && !isTSPrimitive(override) {
			return fmt.Errorf("tstype %q names no generated type", override)
		}
		ts = override
		if _, isSlice := t.Underlying().(*types.Slice); isSlice {
			ts = arrayOf(override)
		}
	case hasOpt(opts, "string"):
		ts = "string"
	default:
		var err error
		if ts, err = e.tsType(t, ""); err != nil {
			return err
		}
		if _, isPtr := t.(*types.Pointer); isPtr && !optional {
			ts += " | null"
		}
	}
	comment(b, "  ", f.Doc)
	q := ""
	if optional {
		q = "?"
	}
	fmt.Fprintf(b, "  %s%s: %s;", propName(jsonName), q, ts)
	if f.Comment != nil {
		fmt.Fprintf(b, " // %s", strings.TrimSpace(strings.ReplaceAll(f.Comment.Text(), "\n", " ")))
	}
	b.WriteString("\n")
	return nil
}

// tsType maps a Go type to its JSON's TypeScript type.
func (e *emitter) tsType(t types.Type, indent string) (string, error) {
	switch t := t.(type) {
	case *types.Alias:
		return e.tsType(types.Unalias(t), indent)
	case *types.Named:
		obj := t.Obj()
		if obj.Pkg() == e.pkg && obj.Exported() {
			return obj.Name(), nil
		}
		switch obj.Pkg().Path() + "." + obj.Name() {
		case "encoding/json.RawMessage":
			return "unknown", nil
		case "time.Time":
			return "string", nil
		}
		return e.tsType(t.Underlying(), indent)
	case *types.Basic:
		return basicTS(t), nil
	case *types.Pointer:
		return e.tsType(t.Elem(), indent)
	case *types.Slice:
		if b, ok := t.Elem().(*types.Basic); ok && b.Kind() == types.Byte {
			return "string", nil // base64
		}
		el, err := e.tsType(t.Elem(), indent)
		return arrayOf(el), err
	case *types.Array:
		el, err := e.tsType(t.Elem(), indent)
		return arrayOf(el), err
	case *types.Map:
		v, err := e.tsType(t.Elem(), indent)
		if err != nil {
			return "", err
		}
		if named := namedOf(t.Key()); named != nil && named.Obj().Pkg() == e.pkg {
			if _, ok := named.Underlying().(*types.Basic); ok {
				return fmt.Sprintf("Partial<Record<%s, %s>>", named.Obj().Name(), v), nil
			}
		}
		return fmt.Sprintf("Record<string, %s>", v), nil
	case *types.Interface:
		return "unknown", nil
	case *types.Struct:
		var b strings.Builder
		b.WriteString("{ ")
		for i := 0; i < t.NumFields(); i++ {
			f := t.Field(i)
			if !f.Exported() {
				continue
			}
			jn, opts := parseJSONTag(reflect.StructTag(t.Tag(i)).Get("json"))
			if jn == "-" && opts == "" {
				continue
			}
			if jn == "" {
				jn = f.Name()
			}
			ft, err := e.tsType(f.Type(), indent)
			if err != nil {
				return "", err
			}
			q := ""
			if hasOpt(opts, "omitempty") || hasOpt(opts, "omitzero") {
				q = "?"
			}
			fmt.Fprintf(&b, "%s%s: %s; ", propName(jn), q, ft)
		}
		b.WriteString("}")
		return b.String(), nil
	}
	return "", fmt.Errorf("no TypeScript form for %s", t)
}

// literal renders a constant-only expression (a constant, or a composite
// literal of them) as a TypeScript value; ok is false for anything computed.
func (e *emitter) literal(x ast.Expr, depth int) (string, bool) {
	if tv, ok := e.info.Types[x]; ok && tv.Value != nil {
		lit, err := constLiteral(tv.Value)
		return lit, err == nil
	}
	cl, ok := x.(*ast.CompositeLit)
	if !ok {
		return "", false
	}
	t := e.info.Types[cl].Type
	switch u := t.Underlying().(type) {
	case *types.Slice, *types.Array:
		parts := make([]string, 0, len(cl.Elts))
		for _, el := range cl.Elts {
			if _, keyed := el.(*ast.KeyValueExpr); keyed {
				return "", false
			}
			v, ok := e.literal(el, depth+1)
			if !ok {
				return "", false
			}
			parts = append(parts, v)
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	case *types.Map:
		parts := make([]string, 0, len(cl.Elts))
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				return "", false
			}
			tv := e.info.Types[kv.Key]
			if tv.Value == nil || tv.Value.Kind() != constant.String {
				return "", false
			}
			v, ok := e.literal(kv.Value, depth+1)
			if !ok {
				return "", false
			}
			parts = append(parts, propName(constant.StringVal(tv.Value))+": "+v)
		}
		return "{ " + strings.Join(parts, ", ") + " }", true
	case *types.Struct:
		given := map[string]ast.Expr{}
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				return "", false
			}
			id, ok := kv.Key.(*ast.Ident)
			if !ok {
				return "", false
			}
			given[id.Name] = kv.Value
		}
		// Every field the JSON carries, in declaration order: an omitted
		// field is its zero value unless omitempty drops it.
		var parts []string
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			jn, opts := parseJSONTag(reflect.StructTag(u.Tag(i)).Get("json"))
			if !f.Exported() || (jn == "-" && opts == "") {
				continue
			}
			if jn == "" {
				jn = f.Name()
			}
			var v string
			if x, ok := given[f.Name()]; ok {
				if v, ok = e.literal(x, depth+1); !ok {
					return "", false
				}
			} else if hasOpt(opts, "omitempty") || hasOpt(opts, "omitzero") {
				continue
			} else if v, ok = zeroLiteral(f.Type()); !ok {
				return "", false
			}
			parts = append(parts, propName(jn)+": "+v)
		}
		return "{ " + strings.Join(parts, ", ") + " }", true
	}
	return "", false
}

func (e *emitter) renderUnions() error {
	if len(e.unionOrder) == 0 {
		return nil
	}
	fmt.Fprintf(&e.b, "\n// ---- %s unions (//wire:union) ----\n", e.pkg.Name())
	for _, name := range e.unionOrder {
		if e.declared[name] {
			return fmt.Errorf("//wire:union %s collides with a type of the same name", name)
		}
		vals := e.unions[name]
		fmt.Fprintf(&e.b, "export type %s = %s;\n", name, strings.Join(vals, " | "))
		fmt.Fprintf(&e.b, "export const %sValues: readonly %s[] = [%s];\n", name, name, strings.Join(vals, ", "))
	}
	return nil
}

var directiveRE = regexp.MustCompile(`^//wire:union ([A-Za-z_][A-Za-z0-9_]*)\s*$`)

// directive returns the union a `//wire:union <Name>` comment names.
func directive(g *ast.CommentGroup) string {
	if g == nil {
		return ""
	}
	for _, c := range g.List {
		if m := directiveRE.FindStringSubmatch(c.Text); m != nil {
			return m[1]
		}
	}
	return ""
}

// comment writes a Go doc comment as TypeScript line comments (directives
// dropped).
func comment(b *bytes.Buffer, indent string, g *ast.CommentGroup) {
	if g == nil {
		return
	}
	text := strings.TrimRight(g.Text(), "\n")
	if text == "" {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			fmt.Fprintf(b, "%s//\n", indent)
			continue
		}
		fmt.Fprintf(b, "%s// %s\n", indent, line)
	}
}

func constLiteral(v constant.Value) (string, error) {
	switch v.Kind() {
	case constant.String:
		raw, err := json.Marshal(constant.StringVal(v))
		return string(raw), err
	case constant.Bool:
		return strconv.FormatBool(constant.BoolVal(v)), nil
	case constant.Int:
		n, exact := constant.Int64Val(v)
		if !exact || n > 1<<53 || n < -(1<<53) {
			return "", fmt.Errorf("integer %s is not a safe JavaScript number", v)
		}
		return strconv.FormatInt(n, 10), nil
	case constant.Float:
		f, _ := constant.Float64Val(v)
		return strconv.FormatFloat(f, 'g', -1, 64), nil
	}
	return "", fmt.Errorf("constant kind %v has no JSON form", v.Kind())
}

// zeroLiteral is the JSON of a basic type's zero value.
func zeroLiteral(t types.Type) (string, bool) {
	b, ok := t.Underlying().(*types.Basic)
	if !ok {
		return "", false
	}
	switch {
	case b.Info()&types.IsString != 0:
		return `""`, true
	case b.Info()&types.IsBoolean != 0:
		return "false", true
	case b.Info()&types.IsNumeric != 0:
		return "0", true
	}
	return "", false
}

func basicTS(b *types.Basic) string {
	switch {
	case b.Info()&types.IsString != 0:
		return "string"
	case b.Info()&types.IsBoolean != 0:
		return "boolean"
	case b.Info()&types.IsNumeric != 0:
		return "number"
	}
	return "unknown"
}

func namedOf(t types.Type) *types.Named {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, _ := types.Unalias(t).(*types.Named)
	return n
}

func arrayOf(el string) string {
	if strings.Contains(el, " | ") || strings.HasPrefix(el, "{") {
		return "(" + el + ")[]"
	}
	return el + "[]"
}

func readonly(ts string) string {
	if strings.HasSuffix(ts, "[]") {
		return "readonly " + ts
	}
	if strings.HasPrefix(ts, "Record<") || strings.HasPrefix(ts, "Partial<") {
		return "Readonly<" + ts + ">"
	}
	return ts
}

func parseJSONTag(tag string) (name, opts string) {
	name, opts, _ = strings.Cut(tag, ",")
	return name, opts
}

func hasOpt(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

var identRE = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

func propName(n string) string {
	if identRE.MatchString(n) {
		return n
	}
	raw, _ := json.Marshal(n)
	return string(raw)
}

func isTSPrimitive(s string) bool {
	switch s {
	case "string", "number", "boolean", "unknown":
		return true
	}
	return false
}
