package loc

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"git.tcp.direct/kayos/common/pool"
	"github.com/BlackEspresso/htmlcheck"
	"github.com/rs/zerolog"
	"golang.org/x/text/language"
	"golang.org/x/tools/go/ast/astutil"
)

const translationDir = "trans"

var bufs = pool.NewBufferFactory()

type Translation struct {
	XMLName xml.Name `xml:"translation"`
	Rows    []Value
	Counter int
}

type Value struct {
	Id      int    `xml:"id,attr"`
	Name    string `xml:"name,attr"`
	Value   string `xml:"value"`
	Comment string `xml:",comment"`
}

type Locer struct {
	DefaultLang string
	Funcs       map[string]struct{}
	Fmtfuncs    map[string]struct{}
	Checked     map[string]struct{}
	OrderedVals []string
	Fset        *token.FileSet
	Apply       bool
	Counter     int64
}

func (l *Locer) Handle(args []string, hdnl func(*ast.File)) error {
	if len(args) == 0 {
		Logger.Error().Msg("No input provided.")
		return nil
	}
	for _, arg := range args {
		fi, err := os.Stat(arg)
		if err != nil {
			return err
		}

		switch mode := fi.Mode(); {
		case mode.IsDir():
			// do directory stuff
			Logger.Debug().Msg("directory input")
			var nodes map[string]*ast.Package
			nodes, err = parser.ParseDir(l.Fset, arg, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			for _, n := range nodes {
				for _, f := range n.Files {
					name := l.Fset.File(f.Pos()).Name()
					if _, ok := l.Checked[name]; ok {
						continue // todo: check for file name clashes in diff packages?
					}
					l.Checked[name] = struct{}{}
					hdnl(f)
				}
			}
		case mode.IsRegular():
			// do file stuff
			Logger.Debug().Msg("file input")
			node, err := parser.ParseFile(l.Fset, arg, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			name := l.Fset.File(node.Pos()).Name()
			if _, ok := l.Checked[name]; ok {
				continue // todo: check for file name clashes in diff packages?
			}
			l.Checked[name] = struct{}{}
			hdnl(node)
		}
	}
	Logger.Info().Msg("the following have been checked:")
	for k := range l.Checked {
		Logger.Info().Msg("  " + k)
	}
	return nil
}

// TODO: remove dup code with the fix() method

func (l *Locer) functionSublogger(x *ast.FuncDecl) zerolog.Logger {
	slog := *Logger
	slog = slog.With().
		Str("caller", x.Name.Name).
		Int("position", int(x.Pos())).
		Str("type", "function").
		Logger()

	if x.Doc.Text() != "" {
		slog = slog.With().Str("doc", x.Doc.Text()).Logger()
	}
	if x.Type.TypeParams != nil && len(x.Type.TypeParams.List) > 0 {
		slog = slog.With().Interface("type_parameters", x.Type.TypeParams).Logger()
	}
	if x.Type.Params != nil && len(x.Type.Params.List) > 0 {
		parameters := make(map[int]string)
		for _, p := range x.Type.Params.List {
			parameters[int(p.Pos())] = p.Type.(*ast.Ident).Name
		}
		slog = slog.With().Interface("parameters", parameters).Logger()
	}
	if x.Type.Results != nil && len(x.Type.Results.List) > 0 {
		results := make(map[int]string)
		for _, r := range x.Type.Results.List {
			results[int(r.Pos())] = r.Type.(*ast.Ident).Name
		}
		slog = slog.With().Interface("results", results).Logger()
	}
	return slog
}

func (l *Locer) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		return nil
	}

	if !n.Pos().IsValid() || !n.End().IsValid() {
		Logger.Warn().
			Interface("node", n).
			Int("pos", int(n.Pos())).
			Msg("invalid position")

		return nil
	}

	switch x := n.(type) {
	case *ast.Comment:
		if x.Text == "" {
			Logger.Trace().Msg("empty comment")
			return l
		}
		return l.HandleComment(x)

	case *ast.CommentGroup:
		for _, c := range x.List {
			l.HandleComment(c)
		}
	case *ast.FuncDecl:
		slog := l.functionSublogger(x)
		_, funcOK := l.Funcs[x.Name.Name]
		_, fmtOK := l.Fmtfuncs[x.Name.Name]
		if funcOK || fmtOK {
			slog.Debug().Msg("found a function in our list")
			return l
		}
		slog.Debug().Msg("found a function")
	case *ast.BasicLit:
		return l.HandleLiteral(x)
	default:
		Logger.Trace().Interface("node", x).Msg("unhandled node")
	}

	return l
}

func (l *Locer) HandleComment(x *ast.Comment) ast.Visitor {
	slog := *Logger

	slog = slog.With().
		Str("type", "comment").
		Str("text", x.Text).
		Logger()

	slog.Trace().Msg("found a comment")

	return l
}

func (l *Locer) HandleLiteral(x *ast.BasicLit) ast.Visitor {
	slog := *Logger

	slog = slog.With().
		Str("type", "literal").
		Interface("value", x.Value).
		Str("kind", x.Kind.String()).
		Logger()

	slog.Trace().Msg("found a literal")

	switch {
	case x.Kind.IsOperator():
		slog.Debug().Msg("ignoring operator during literal inspection")
		return l
	case x.Kind.IsKeyword():
		slog.Debug().Msg("ignoring keyword during literal inspection")
		return l
	case x.Kind.Precedence() > token.LowestPrec:
		slog.Debug().Msg("ignoring binary op during literal inspection")
		return l
	case x.Kind != token.STRING:
		slog.Debug().Msg("ignoring non-string during literal inspection")
		return l
	case x.Kind == token.STRING:
		//
	default:
		slog.Warn().Msg("unhandled literal")
		return l
	}

	return l.HandleString(x)
}

func (l *Locer) HandleString(x *ast.BasicLit) ast.Visitor {
	slog := *Logger

	slog = slog.With().
		Str("type", "string").
		// Interface("value", x.Value).
		Logger()

	slog.Trace().Msg("processing string literal")

	buf := bufs.Get()

	if err := printer.Fprint(buf, l.Fset, x); err != nil {
		bufs.MustPut(buf)
		slog.Fatal().Err(err).Send()
		return nil // unreachable
	}

	if strings.TrimSpace(buf.String()) == "" {
		slog.Debug().Msg("ignoring empty string")
		bufs.MustPut(buf)
		return l
	}

	slog.Info().Msgf("found: %s", buf.String())

	bufs.MustPut(buf)

	atomic.AddInt64(&l.Counter, 1)
	name := l.Fset.File(x.Pos()).Name() + ":" + strconv.Itoa(int(atomic.LoadInt64(&l.Counter)))
	l.OrderedVals = append(l.OrderedVals, name)

	return l
}

func (l *Locer) Inspect(node *ast.File) {
	// var inMeth *ast.FuncDecl
	ast.Walk(l, node)

	/*if ret, ok := n.(*ast.CallExpr); ok {
		slog = slog.With().Str("type", "call").Logger()
		slog.Trace().Msg("found a call")
		// printer.Fprint(os.Stdout, fset, ret)
		if fnc, fncOK := ret.Fun.(*ast.SelectorExpr); fncOK {
			if fnc.Sel != nil && fnc.Sel.Name != "" {
				slog = slog.With().
					Str("caller", fnc.Sel.Name).
					Int("position", int(fnc.Sel.Pos())).
					Logger()
			}
			slog.Debug().Msg("found a selector")
			if contains(append(l.Funcs, l.Fmtfuncs...), fnc.Sel.Name) && len(ret.Args) > 0 {
				for _, ex := range ret.Args {
				if v, bLitOK := ex.(*ast.BasicLit); bLitOK && v.Kind == token.STRING {
					buf := bufs.Get()
					if err := printer.Fprint(buf, l.Fset, v); err != nil {
						slog.Fatal().Err(err).Send()
					}
					slog.Debug().Msgf("found string: %s", buf.String())
					bufs.MustPut(buf)

					counter++
					name := l.Fset.File(v.Pos()).Name() + ":" + strconv.Itoa(counter)
					l.OrderedVals = append(l.OrderedVals, name)

				} else if v2, binOK := ex.(*ast.BinaryExpr); binOK && v2.Op == token.ADD {
					// note: plz reformat not to use adds
					slog.Debug().Msg("found a binary expr instead of str; fix your code")
					// v, ok := v2.X.(*ast.BasicLit)
					// v, ok := v2.Y.(*ast.BasicLit)

				} else {
					slog.Debug().Msgf("found something else: %T", ex)
				}
				}
			}
		}
	}*/

}

var newData map[string]map[string]map[string]Value // locale:(filename:(trigger:Value))
var newDataNames map[string][]string               // filename:[]newtriggers
var noDupStrings map[string]string                 // map of currently loaded strings, to avoid duplicates and reduce translation efforts

// todo: ensure import works as expected

func (l *Locer) Fix(node *ast.File) {
	name := l.Fset.File(node.Pos()).Name()
	loadModuleExpr := &ast.ExprStmt{
		X: &ast.CallExpr{
			Fun: &ast.SelectorExpr{
				X:   &ast.Ident{Name: "goloc"},
				Sel: &ast.Ident{Name: "Load"},
			},
			Args: []ast.Expr{
				&ast.BasicLit{
					Kind:  token.STRING,
					Value: strconv.Quote(name),
				},
			},
		},
	}

	// todo: investigate unnecessary "lang := " loads

	Load(name) // load current values
	Logger.Debug().Msgf("module count at %d", dataCount[name])
	newData = make(map[string]map[string]map[string]Value) // locale:(filename:(trigger:Value))
	newDataNames = make(map[string][]string)               // filename:[]newtriggers
	noDupStrings = make(map[string]string)                 // map of currently loaded strings, to avoid duplicates and reduce translation efforts

	// make sure default language is loaded
	newData[l.DefaultLang] = make(map[string]map[string]Value)
	newData[l.DefaultLang][name] = make(map[string]Value)
	// initialise set for all other languages
	for k := range data { // initialise all languages
		newData[k] = make(map[string]map[string]Value)
		newData[k][name] = make(map[string]Value)
	}

	var needsLangSetting bool  // method needs the lang := arg
	var needGolocImport bool   // goloc needs importing
	var needStrconvImport bool // need to import strconv
	var initExists bool        // does init method exist

	// should return to node?
	astutil.Apply(node,
		/*pre*/
		func(cursor *astutil.Cursor) bool {
			n := cursor.Node()
			// get info on init call.
			if ret, ok := n.(*ast.FuncDecl); ok {
				if ret.Name.Name == "init" {
					initExists = true
				}

				// Check method calls
			} else if callExpr, ok := n.(*ast.CallExpr); ok {
				// determine if method is one of the validated ones
				if funcCall, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
					Logger.Debug().Msg("found random call named " + funcCall.Sel.Name)

					// if valid and has args, check first arg (which should be a string)
					_, funcOK := l.Funcs[funcCall.Sel.Name]
					_, fmtOK := l.Fmtfuncs[funcCall.Sel.Name]
					if funcOK || fmtOK && len(callExpr.Args) > 0 {
						firstArg := callExpr.Args[0]

						if litItem, ok := firstArg.(*ast.BasicLit); ok && litItem.Kind == token.STRING {
							buf := bytes.NewBuffer([]byte{})
							if err := printer.Fprint(buf, l.Fset, litItem); err != nil {
								Logger.Fatal().Err(err).Send()
							}
							Logger.Debug().Msgf("found a string in funcname %s:\n%s", funcCall.Sel.Name, buf.String())

							args, needStrconvImportNew := l.injectTran(name, callExpr, funcCall, litItem)

							funcCall.Sel.Name = l.getUnFmtFunc(funcCall.Sel.Name)
							callExpr.Fun = funcCall
							callExpr.Args = []ast.Expr{args}
							cursor.Replace(callExpr)
							needStrconvImport = needStrconvImportNew
							needGolocImport = true
							needsLangSetting = true
							return false

							// if not a string, but a binop:
						} else if binExpr, ok := firstArg.(*ast.BinaryExpr); ok && binExpr.Op == token.ADD {
							// note: plz reformat not to use adds
							Logger.Debug().Msg("found a binary expr instead of str; fix your code")

						} else {
							Logger.Debug().Msgf("found something else: %T", firstArg)
						}
					} else if caller, ok := funcCall.X.(*ast.Ident); ok && caller.Name == "goloc" {
						// has already been translated, check if it isn't duplicated.
						if funcCall.Sel.Name == "Trnl" || funcCall.Sel.Name == "Trnlf" {
							if arg, ok := callExpr.Args[1].(*ast.BasicLit); ok && arg.Kind == token.STRING { // possible OOB
								val, err := strconv.Unquote(arg.Value)
								if err != nil {
									Logger.Fatal().Err(err).Send()
									return true
								}
								itemName, ok := noDupStrings[data[l.DefaultLang][val].Value]
								if ok {
									val = itemName
								} else {
									noDupStrings[data[l.DefaultLang][val].Value] = val
									// add curr data to the new data (this will remove unused vals)
									for lang := range newData {
										currVal, ok := data[lang][val]
										if !ok {
											defLangVal := data[l.DefaultLang][val]
											currVal = Value{
												Id:      defLangVal.Id,
												Name:    defLangVal.Name,
												Value:   "",
												Comment: defLangVal.Value,
											}
											// add to old data list, so its added at the start and offsets aren't changed.
										}
										newData[lang][name][val] = currVal
									}
								}

								arg.Value = strconv.Quote(val)
								cursor.Replace(n)
								return false
							}
						} else if funcCall.Sel.Name == "Add" || funcCall.Sel.Name == "Addf" {
							if v, ok := callExpr.Args[0].(*ast.BasicLit); ok {
								buf := bytes.NewBuffer([]byte{})
								printer.Fprint(buf, l.Fset, v)
								Logger.Debug().Msgf("found a string to add via Add(f):\n%s", buf.String())

								callExpr, needStrconvImport = l.injectTran(name, callExpr, funcCall, v)

								cursor.Replace(callExpr)
								needGolocImport = true
								needsLangSetting = true
								return false
							}
						}
					}
				}
			}

			return true
		},
		/*post*/
		func(cursor *astutil.Cursor) bool {
			if FuncDecl, ok := cursor.Node().(*ast.FuncDecl); ok && needsLangSetting {
				if initExists && FuncDecl.Name.Name == "init" && needGolocImport {
					if !initHasLoad(FuncDecl, name) {
						FuncDecl.Body.List = append(FuncDecl.Body.List, loadModuleExpr)
						cursor.Replace(FuncDecl)
					}
				}

				if len(FuncDecl.Body.List) == 0 {
					return true // do nothing
				} else if ass, ok := FuncDecl.Body.List[0].(*ast.AssignStmt); ok {
					// todo: stronger check
					if i, ok := ass.Lhs[0].(*ast.Ident); ok && i.Name == "lang" { // check/update generator
						return true // continue and ignore
					}
				}

				Logger.Debug().Msg("adding lang to " + name)
				FuncDecl.Body.List = append([]ast.Stmt{
					&ast.AssignStmt{
						Lhs: []ast.Expr{&ast.Ident{Name: "lang"}},
						Tok: token.DEFINE,
						Rhs: []ast.Expr{
							&ast.CallExpr{
								Fun:  &ast.Ident{Name: "getLang"},       // todo: parameterise
								Args: []ast.Expr{&ast.Ident{Name: "u"}}, // todo: figure this bit out
							},
						},
					},
				}, FuncDecl.Body.List...)
				cursor.Replace(FuncDecl)
				needsLangSetting = false
			}
			return true
		},
	)

	astutil.Apply(node, func(cursor *astutil.Cursor) bool {
		return true
	}, func(cursor *astutil.Cursor) bool {
		if d, ok := cursor.Node().(*ast.GenDecl); ok && d.Tok == token.IMPORT && !initExists && needGolocImport {
			v := &ast.FuncDecl{
				Name: &ast.Ident{Name: "init"},
				Type: &ast.FuncType{Params: &ast.FieldList{List: []*ast.Field{}}},
				Body: &ast.BlockStmt{List: []ast.Stmt{loadModuleExpr}},
			}
			cursor.InsertAfter(v)
		} else if ret, ok := cursor.Node().(*ast.FuncDecl); ok && needsLangSetting {
			if initExists && ret.Name.Name == "init" && needGolocImport {
				if !initHasLoad(ret, name) {
					ret.Body.List = append(ret.Body.List, loadModuleExpr)
					cursor.Replace(ret)
				}
			}
		}
		return true
	})

	if needGolocImport {
		astutil.AddImport(l.Fset, node, "github.com/PaulSonOfLars/goloc")
		ast.SortImports(l.Fset, node)
	}

	if needStrconvImport {
		astutil.AddImport(l.Fset, node, "strconv")
		ast.SortImports(l.Fset, node)
	}

	out := os.Stdout
	if l.Apply {
		f, err := os.Create(name)
		if err != nil {
			Logger.Fatal().Err(err).Send()
			return
		}
		defer f.Close()
		// set file output
		out = f

	}
	if err := format.Node(out, l.Fset, node); err != nil {
		Logger.Fatal().Err(err).Send()
		return
	}
	if err := l.saveMap(newData, newDataNames); err != nil {
		Logger.Fatal().Err(err).Send()
		return
	}
}

func (l *Locer) Create(args []string, lang language.Tag) {
	err := filepath.Walk(path.Join(translationDir, l.DefaultLang),
		func(fpath string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}

			f, err := os.Open(fpath)
			if err != nil {
				return err
			}
			defer f.Close()
			dec := xml.NewDecoder(f)
			var xmlData Translation
			err = dec.Decode(&xmlData)
			if err != nil {
				return err
			}

			for i := 0; i < len(xmlData.Rows); i++ {
				xmlData.Rows[i].Comment = xmlData.Rows[i].Value
				xmlData.Rows[i].Value = ""
			}

			filename := strings.Replace(fpath, sep(l.DefaultLang), sep(lang.String()), 1)

			if err := os.MkdirAll(path.Dir(filename), 0755); err != nil {
				return err
			}

			newF, err := os.Create(filename)
			if err != nil {
				return err
			}
			defer newF.Close()
			newF.WriteString(xml.Header)
			enc := xml.NewEncoder(newF)
			enc.Indent("", "    ")
			if err := enc.Encode(xmlData); err != nil {
				return err
			}
			newF.WriteString("\n")
			return nil
		})
	if err != nil {
		Logger.Fatal().Err(err).Send()
	}
}

func (l *Locer) CheckAll() error {
	LoadAll(l.DefaultLang)

	v := getHTMLValidator()
	for lang := range data {
		if err := l.check(v, lang); err != nil {
			return err
		}
	}
	return nil
}

func (l *Locer) Check(lang string) error {
	LoadLangAll(l.DefaultLang)
	LoadLangAll(lang)

	err := l.check(getHTMLValidator(), lang)
	if err != nil {
		return fmt.Errorf("error found for %s: %w", lang, err)
	}
	return nil
}

func getHTMLValidator() htmlcheck.Validator {
	v := htmlcheck.Validator{}
	v.AddValidTags([]*htmlcheck.ValidTag{
		{Name: "b"}, {Name: "strong"},
		{Name: "i"}, {Name: "em"},
		{Name: "u"}, {Name: "ins"},
		{Name: "s"}, {Name: "strike"}, {Name: "del"},
		{
			Name:  "a",
			Attrs: []string{"href"},
		},
		{
			Name:  "code",
			Attrs: []string{"class"},
		},
		{Name: "pre"},
	})
	return v
}

func (l *Locer) check(v htmlcheck.Validator, lang string) error {
	if lang == l.DefaultLang { // don't check default
		return nil
	}

	for s, d := range data[lang] {
		if s != d.Name {
			Logger.Error().Msgf("%s: '%s'\tfatally incorrect", lang, s)
			continue
		}
		defLangVal := data[l.DefaultLang][s]

		if defLangVal.Id != d.Id {
			Logger.Error().Msgf("%s: '%s'\thas different ids from default language %s", lang, s, l.DefaultLang)
			continue
		}

		if defLangVal.Value == d.Value {
			// Same; skip.
			continue
		}

		if err := checkCurlies(defLangVal.Value, d.Value); err != nil {
			Logger.Error().Msgf("%s: '%s'\tcurlies mismatch: %s", lang, s, err.Error())

		}
		if err := checkValidHTML(v, defLangVal.Value, d.Value); err != nil {
			Logger.Error().Msgf("%s: '%s'\tHTML error: %s", lang, s, err.Error())
		}
		// if err := checkWS(defLangVal.Value, d.Value); err != nil {
		//	Logger.Error().Msgf("%s: '%s'\twhitespace error: %s", lang, s, err.Error())
		// }
		if err := checkForSymbols(defLangVal.Value, d.Value); err != nil {
			Logger.Error().Msgf("%s: '%s'\tsymbols error: %s", lang, s, err.Error())
		}
	}
	// TODO: investigate changing decoder
	return nil
}

func checkValidHTML(v htmlcheck.Validator, def string, custom string) error {
	errs := v.ValidateHtmlString(custom)
	if len(errs) == 0 {
		return nil
	}

	sourceHasInvalidTags := false
	invTags := 0

	defErrs := v.ValidateHtmlString(def)
	if len(defErrs) != 0 {
		for _, e := range defErrs {
			if e.Reason == htmlcheck.InvTag {
				sourceHasInvalidTags = true
				invTags++
			}
		}
		for _, e := range errs {
			if e.Reason == htmlcheck.InvTag {
				invTags--
			}
		}
	}

	if len(errs) == 1 {
		if sourceHasInvalidTags && invTags == 0 {
			// ignore
			return nil
		}

		return fmt.Errorf("html error in custom string: %w", errs[0])
	}

	return fmt.Errorf("%d html errors in custom string", len(errs))
}

var wsStartRex = regexp.MustCompile(`(?m)^\s*`)
var wsEndRex = regexp.MustCompile(`(?m)\s*$`)

func checkWS(def string, custom string) error {
	pref := wsStartRex.FindString(def)
	if !strings.HasPrefix(custom, pref) {
		return fmt.Errorf("mismatched whitespace prefix")
	}

	suff := wsEndRex.FindString(def)
	if !strings.HasSuffix(custom, suff) {
		return fmt.Errorf("mismatched whitespace suffix")
	}

	return nil
}

func checkForSymbols(def string, custom string) error {
	if strings.Count(def, "@") != strings.Count(custom, "@") {
		return errors.New("unexpected number of @'s")
	}

	return nil
}

var curliesRex = regexp.MustCompile(`\{\d+?\}`)

// Basic regex check to see if expected matches. Should be good enough for now.
func checkCurlies(def string, custom string) error {
	defMatches := curliesRex.FindAllStringSubmatch(def, -1)
	customMatches := curliesRex.FindAllStringSubmatch(custom, -1)

	if len(defMatches) != len(customMatches) {
		return fmt.Errorf("different number of format tags (should be %d, got %d)", len(defMatches), len(customMatches))
	}

	matches := map[string]int{}
	for _, d := range defMatches {
		matches[d[0]]++
	}

	for _, d := range customMatches {
		m := d[0]
		if _, ok := matches[m]; !ok {
			return fmt.Errorf("unknown curly %s in custom string", m)
		}
		matches[m]--
	}

	for s, c := range matches {
		if c == 0 {
			continue
		}

		if c > 0 {
			return fmt.Errorf("not enough uses of %s", s)
		}
		if c < 0 {
			return fmt.Errorf("too many uses of %s", s)
		}

	}
	return nil
}

func (l *Locer) getUnFmtFunc(name string) string {
	if !strings.HasSuffix(name, "f") {
		// not a formatting function; all ok.
		return name
	}
	if _, ok := l.Fmtfuncs[name[:len(name)-1]]; ok {
		// found simple func; return.
		return name[:len(name)-1]
	}
	// no result found; return current.
	return name
}
