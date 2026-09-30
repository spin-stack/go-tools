package mutate

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// oneProc is a module whose one test passes only in a process held to one core, which is what
// Share(more jobs than cores) gives every test process.
func oneProc(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":  "module example.com/oneproc\n\ngo 1.24\n",
		"code.go": "package oneproc\n\nfunc One() int {\n\treturn 1\n}\n",
		"code_test.go": "package oneproc\n\nimport (\n\t\"runtime\"\n\t\"testing\"\n)\n\n" +
			"func TestHeldToOneCore(t *testing.T) {\n\tif runtime.GOMAXPROCS(0) != 1 || One() != 1 {\n\t\tt.Fatal(\"not held to one core\")\n\t}\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// share is Share for one test, the machine's again after it.
func share(t *testing.T, jobs int) {
	t.Helper()
	Share(jobs, -1)
	t.Cleanup(func() { testProcs, memLimit = 0, 0 })
}

// The machine is divided among the jobs that run at once, and a process never gets less than a
// core.
func TestEachJobIsHeldToItsShareOfTheCores(t *testing.T) {
	share(t, 2)
	if want := max(runtime.NumCPU()/2, 1); testProcs != want {
		t.Errorf("two jobs are held to %d core(s) each, want %d", testProcs, want)
	}
	// One job, or a -j of nothing, is the whole machine.
	for _, jobs := range []int{1, 0} {
		share(t, jobs)
		if testProcs != runtime.NumCPU() {
			t.Errorf("-j %d holds its job to %d core(s), want all %d", jobs, testProcs, runtime.NumCPU())
		}
	}
	share(t, 1<<20)
	env := testEnv("A=b")
	if !slices.Contains(env, "GOMAXPROCS=1") || env[len(env)-1] != "A=b" {
		t.Errorf("more jobs than cores: the environment ends %q", env[max(len(env)-3, 0):])
	}
	testProcs = 0
	if slices.Contains(testEnv(), "GOMAXPROCS=1") && os.Getenv("GOMAXPROCS") != "1" {
		t.Error("a run that shares nothing held its tests to one core")
	}
}

// Every way a test process is started holds it to its share: measuring coverage, asking a schema
// binary, and building an edit alone.
func TestEveryTestProcessIsHeldToItsShare(t *testing.T) {
	dir := oneProc(t)
	share(t, 1<<20)

	c, err := Cover(dir, "1m", make(chan struct{}, 1))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Reaching(filepath.Join(dir, "code.go"), 4); !slices.Equal(got, []string{"TestHeldToOneCore"}) {
		t.Errorf("coverage ran its test on every core: the line is reached by %q", got)
	}

	bin := filepath.Join(t.TempDir(), "pkg.test")
	build := exec.Command("go", "test", "-c", "-o", bin, ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if outcome, out := Ask(bin, dir, 0, "1m", nil); outcome != Survived {
		t.Errorf("a schema binary was asked on every core: %s", out)
	}

	if outcome, out := Run(Mutation{File: filepath.Join(dir, "code.go")}, Asked{Timeout: "1m"}); outcome != Survived {
		t.Errorf("an edit built alone was asked on every core: %s", out)
	}

	// And each says which tests it ran, under the package's directory from its module's root:
	// the schema binary was asked by the directory, go test named the import path.
	asked := map[string]int{}
	for _, c := range TestCosts() {
		if c.Test == "TestHeldToOneCore" {
			asked[c.Package] = c.N
		}
	}
	if asked["."] != 1 || asked["example.com/oneproc"] != 1 {
		t.Errorf("the asked tests were not counted to their packages: %v", asked)
	}
}
