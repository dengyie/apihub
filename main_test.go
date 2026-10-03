package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedFrontendThresholdsMatchMakefile 把「阈值三处一致」变成可执行断言。
//
// 白屏事故的成因不是某一处阈值写错了，而是闸门只建在 CI 这一条路径上、且
// main.go / Makefile / workflow 三处的数字没有任何一处能对照到另一处。改一处
// 忘了另两处，下一次构建就会悄悄产出一个 UI 残缺的二进制。
func TestEmbeddedFrontendThresholdsMatchMakefile(t *testing.T) {
	mk, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatalf("读不到 Makefile：%v", err)
	}
	text := string(mk)
	for _, want := range []string{"-lt 200", "-lt 10"} {
		if !strings.Contains(text, want) {
			t.Fatalf("Makefile 的 verify-embed 缺少 %q，与 main.go 的阈值不一致；"+
				"改一处必须同时改三处", want)
		}
	}
	if !strings.Contains(text, "verify-embed") {
		t.Fatal("Makefile 缺少 verify-embed 目标：裸 go build 不受任何检查")
	}
	if !strings.Contains(text, "build-api: verify-embed") {
		t.Fatal("build-api 必须依赖 verify-embed，否则本地构建二进制绕过了闸门")
	}
}

// TestCheckEmbeddedFrontendRejectsPlaceholderBundle 直接驱动判定逻辑。
//
// 事故形态：web/dist/index.html 存在但只有 0 字节，static 目录也在却只有一两个
// 文件。go:embed 对「目录不存在」「目录为空」都会报错，那两种情况天然安全；只有
// 「文件存在但内容是占位」才会安静地产出一个能启动、能响应 200、首页正文 0 字节
// 的二进制 —— 后台白屏，且状态码查不出来。
func TestCheckEmbeddedFrontendRejectsPlaceholderBundle(t *testing.T) {
	cases := []struct {
		name       string
		index      []byte
		staticFile int
		wantReject bool
	}{
		{"占位 index.html + 空 static", []byte{}, 0, true},
		{"index 够大但资源缺失", make([]byte, 1024), 2, true},
		{"资源齐全但 index 是占位", []byte("<html></html>"), 20, true},
		{"真实构建（线上实测 1047 字节）", make([]byte, 1047), 20, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := checkEmbeddedFrontend(embeddedFrontend{
				indexPage: tc.index, staticFileCount: tc.staticFile,
			})
			if tc.wantReject && len(problems) == 0 {
				t.Fatal("占位/残缺的前端必须被拒，否则会产出白屏二进制")
			}
			if !tc.wantReject && len(problems) != 0 {
				t.Fatalf("真实构建不应被拒，却报出：%v", problems)
			}
		})
	}
}

// TestCheckEmbeddedFrontendCountsFilesNotDirs 数的是文件数，不是目录数 ——
// 一个只有空子目录的 static/ 不该被当成有资源。
func TestCheckEmbeddedFrontendCountsFilesNotDirs(t *testing.T) {
	dir := t.TempDir()
	// 真实构建的 static 资源在两位数以上，取 12 个刚好越过下限
	for i := 0; i < 12; i++ {
		p := filepath.Join(dir, "chunk", iName(i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 另造一个只含空目录的 static，验证目录不计入
	onlyDirs := filepath.Join(t.TempDir(), "static")
	if err := os.MkdirAll(filepath.Join(onlyDirs, "js"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		dir      string
		want     int
		rejected bool
	}{
		{dir, 12, false},
		{onlyDirs, 0, true},
	} {
		got := countStaticFiles(os.DirFS(tc.dir), ".")
		if got != tc.want {
			t.Fatalf("staticFileCount 期望 %d，got %d", tc.want, got)
		}
		problems := checkEmbeddedFrontend(embeddedFrontend{
			indexPage: make([]byte, 1024), staticFileCount: got,
		})
		if (len(problems) != 0) != tc.rejected {
			t.Fatalf("staticFileCount=%d 时判定应为 rejected=%v，实际 %v", got, tc.rejected, problems)
		}
	}
}

func iName(i int) string {
	return "index." + string(rune('a'+i)) + ".chunk"
}
