// Package code 는 app/ 의 Go 코드 자체가 지키는 규칙을 소스를 파싱해 확인하는 테스트다 (스택 없이 돈다).
package code

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// modulePath 는 app/ 모듈 경로다. 이 아래 import 만 패키지 사이의 사용으로 센다.
const modulePath = "github.com/drakejin/workspace/app"

// sourceFile 은 파싱한 .go 파일 하나다. 빌드 태그(e2e·e2e_slow·e2e_s3)와 상관없이 모든 파일을 읽는다.
type sourceFile struct {
	dir  string // app/ 기준 슬래시 경로 (internal/ops)
	test bool
	file *ast.File
}

// declKey 는 패키지 수준 선언 하나다 (디렉토리 + 이름).
type declKey struct{ dir, name string }

// exportedDecls 는 internal/ 운영 코드의 exported 패키지 수준 선언과 그 타입의 exported 메서드다.
type exportedDecls struct {
	nodes   map[declKey]ast.Node        // func 는 *ast.FuncDecl, 나머지는 *ast.TypeSpec·*ast.ValueSpec
	types   map[declKey]bool            // 타입 선언만
	methods map[declKey][]*ast.FuncDecl // 타입 → exported 메서드
	pkgName map[string]string           // 디렉토리 → 패키지 이름
}

// DEAD-12: internal/ 의 exported 패키지 수준 식별자(func·const·var·type)는 다른 디렉토리(테스트·e2e 포함)가 쓰는 것만이다.
// 자기 패키지(그 패키지의 테스트 포함) 안에서만 쓰면 비공개로 둔다 — internal 이라 밖에 새지는 않지만 패키지 경계가 흐려진다.
// 타입은 이름으로 불리지 않아도, 다른 패키지가 쓰는 exported 선언(시그니처·exported 필드·exported 메서드)에 나오면 쓰이는 것으로 본다.
func TestInternalExportsAreUsedOutsideTheirPackage(t *testing.T) {
	files := parseModule(t)
	decls := collectExported(files)
	if len(decls.nodes) == 0 {
		t.Fatal("internal/ 의 exported 선언을 하나도 찾지 못했다 — app/ 경로를 잘못 읽었다")
	}
	used := apiClosure(decls, usedFromOtherDirs(files, decls.pkgName))

	var unused []string
	for k := range decls.nodes {
		if !used[k] {
			unused = append(unused, k.dir+"."+k.name)
		}
	}
	sort.Strings(unused)
	for _, u := range unused {
		t.Errorf("%s: 자기 패키지 밖에서 쓰지 않는다 — 비공개(소문자)로 바꾼다", u)
	}
}

// parseModule 은 app/ 아래 .go 파일을 모두 파싱한다.
func parseModule(t *testing.T) []sourceFile {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("app root: %v", err)
	}
	fset := token.NewFileSet()
	var files []sourceFile
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != root && ignoredByGo(d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() || filepath.Ext(p) != ".go" {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		files = append(files, sourceFile{dir: filepath.ToSlash(rel), test: strings.HasSuffix(p, "_test.go"), file: f})
		return nil
	})
	if err != nil {
		t.Fatalf("parse %s: %v", root, err)
	}
	return files
}

// ignoredByGo 는 go 명령이 패키지로 보지 않는 디렉토리다 (testdata, . 이나 _ 로 시작하는 이름).
func ignoredByGo(name string) bool {
	return name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// collectExported 는 internal/ 운영 파일(_test.go 가 아닌 것)의 exported 선언을 모은다.
func collectExported(files []sourceFile) exportedDecls {
	d := exportedDecls{nodes: map[declKey]ast.Node{}, types: map[declKey]bool{}, methods: map[declKey][]*ast.FuncDecl{}, pkgName: map[string]string{}}
	for _, sf := range files {
		if sf.test {
			continue
		}
		d.pkgName[sf.dir] = sf.file.Name.Name
		if !strings.HasPrefix(sf.dir, "internal/") {
			continue
		}
		for _, decl := range sf.file.Decls {
			d.addDecl(sf.dir, decl)
		}
	}
	return d
}

func (d exportedDecls) addDecl(dir string, decl ast.Decl) {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		if !decl.Name.IsExported() {
			return
		}
		if decl.Recv == nil {
			d.nodes[declKey{dir, decl.Name.Name}] = decl
			return
		}
		recv := declKey{dir, receiverType(decl.Recv.List[0].Type)}
		d.methods[recv] = append(d.methods[recv], decl)
	case *ast.GenDecl:
		for _, spec := range decl.Specs {
			switch spec := spec.(type) {
			case *ast.TypeSpec:
				if spec.Name.IsExported() {
					d.nodes[declKey{dir, spec.Name.Name}] = spec
					d.types[declKey{dir, spec.Name.Name}] = true
				}
			case *ast.ValueSpec:
				for _, n := range spec.Names {
					if n.IsExported() {
						d.nodes[declKey{dir, n.Name}] = spec
					}
				}
			}
		}
	}
}

// receiverType 은 메서드 수신자의 타입 이름이다 (*T, T[K] → T).
func receiverType(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

// usedFromOtherDirs 는 다른 디렉토리의 파일이 pkg.Name 으로 부른 internal 선언이다.
func usedFromOtherDirs(files []sourceFile, pkgName map[string]string) map[declKey]bool {
	used := map[declKey]bool{}
	for _, sf := range files {
		imports := moduleImports(sf.file, pkgName)
		ast.Inspect(sf.file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// Obj != nil 이면 지역 이름이 패키지 이름을 가린 것이다
			if id, ok := sel.X.(*ast.Ident); ok && id.Obj == nil && imports[id.Name] != "" && imports[id.Name] != sf.dir {
				used[declKey{imports[id.Name], sel.Sel.Name}] = true
			}
			return true
		})
	}
	return used
}

// moduleImports 는 파일 안의 이름 → 모듈 안 import 디렉토리다.
func moduleImports(f *ast.File, pkgName map[string]string) map[string]string {
	imports := map[string]string{}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !strings.HasPrefix(p, modulePath+"/") {
			continue
		}
		dir := strings.TrimPrefix(p, modulePath+"/")
		name := pkgName[dir]
		if name == "" {
			name = path.Base(dir)
		}
		if imp.Name != nil {
			name = imp.Name.Name
		}
		imports[name] = dir
	}
	return imports
}

// apiClosure 는 직접 쓰인 선언에, 그 선언의 exported 면(시그니처·exported 필드·exported 메서드)에 나오는
// 같은 패키지 타입을 따라가며 더한 집합이다. 새 map 을 돌려준다.
func apiClosure(d exportedDecls, direct map[declKey]bool) map[declKey]bool {
	used := map[declKey]bool{}
	var queue []declKey
	for k := range direct {
		if d.nodes[k] != nil {
			queue = append(queue, k)
		}
	}
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		if used[k] {
			continue
		}
		used[k] = true
		for _, node := range surface(d, k) {
			queue = append(queue, typeRefs(d, k.dir, node)...)
		}
	}
	return used
}

// surface 는 선언 k 가 다른 패키지에 드러내는 부분이다. 함수 본문과 비공개 필드는 뺀다.
func surface(d exportedDecls, k declKey) []ast.Node {
	var nodes []ast.Node
	switch n := d.nodes[k].(type) {
	case *ast.FuncDecl:
		nodes = append(nodes, n.Type)
	case *ast.ValueSpec:
		if n.Type != nil {
			nodes = append(nodes, n.Type)
		}
		for _, v := range n.Values {
			nodes = append(nodes, v)
		}
	case *ast.TypeSpec:
		nodes = append(nodes, typeSurface(n)...)
	}
	for _, m := range d.methods[k] {
		nodes = append(nodes, m.Type)
	}
	return nodes
}

// typeSurface 는 타입 선언에서 밖에 보이는 부분이다 — 구조체면 exported·임베디드 필드의 타입만.
func typeSurface(spec *ast.TypeSpec) []ast.Node {
	st, ok := spec.Type.(*ast.StructType)
	if !ok {
		return []ast.Node{spec.Type}
	}
	var nodes []ast.Node
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 || anyExported(f.Names) {
			nodes = append(nodes, f.Type)
		}
	}
	return nodes
}

func anyExported(names []*ast.Ident) bool {
	for _, n := range names {
		if n.IsExported() {
			return true
		}
	}
	return false
}

// typeRefs 는 node 안에서 같은 패키지(dir)의 exported 타입을 가리키는 이름이다. pkg.Name 의 Name 은 다른 패키지라 뺀다.
func typeRefs(d exportedDecls, dir string, node ast.Node) []declKey {
	var refs []declKey
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			refs = append(refs, typeRefs(d, dir, x.X)...)
			return false
		case *ast.Ident:
			if k := (declKey{dir, x.Name}); d.types[k] {
				refs = append(refs, k)
			}
		}
		return true
	})
	return refs
}
