package cmdclass

import "testing"

func TestClassifyInvestigateCommands(t *testing.T) {
	tests := []string{
		"cat README.md",
		"less SPEC.md",
		"head -n 20 go.mod",
		"tail -n 50 app.log",
		"grep -R winglet internal",
		"rg --files",
		"find internal -name '*.go'",
		"fd SPEC",
		"ls -la",
		"wc -l README.md",
		"git status --short",
		"git diff -- README.md",
		"git log --oneline -5",
		"git show HEAD:README.md",
		"git grep Classify",
		"go test -list TestClassify ./internal/cmdclass",
		"go test -list=TestClassify ./internal/cmdclass",
		"curl https://example.com",
		"wget https://example.com/file.txt",
	}

	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			if got := Classify(command); got != Investigate {
				t.Fatalf("Classify(%q) = %v, want Investigate", command, got)
			}
		})
	}
}

func TestClassifyNeutralCommands(t *testing.T) {
	tests := []string{
		"",
		"   ",
		"rg winglet && go test ./...",
		"rg winglet || true",
		"rg winglet; go test ./...",
		"rg winglet | wc -l",
		"cat README.md > out.txt",
		"cat < README.md",
		"echo $(pwd)",
		"echo `pwd`",
		"unknown README.md",
		"npm install",
		"pnpm test",
		"yarn build",
		"bun install",
		"cargo test",
		"pip install requests",
		"uv sync",
		"git commit -m msg",
		"git checkout -b branch",
		"go test ./...",
		"go build ./...",
		"sed -i '' 's/a/b/' file.txt",
		"perl -i -pe 's/a/b/' file.txt",
		"mv a b",
		"cp a b",
		"rm file.txt",
		"mcp__github__get_issue",
		"tail -f app.log",
		"find . -delete",
		"find . -exec rm {} +",
		"curl -o out.txt https://example.com",
		"curl --output=out.txt https://example.com",
		"curl -T file.txt https://example.com",
		"wget -O out.txt https://example.com",
		"wget --directory-prefix downloads https://example.com/file.txt",
	}

	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			if got := Classify(command); got != Neutral {
				t.Fatalf("Classify(%q) = %v, want Neutral", command, got)
			}
		})
	}
}

func TestClassifyNeverReturnsImplementForBashCommands(t *testing.T) {
	tests := []string{
		"apply_patch",
		"apply_patch <<'PATCH'",
		"Edit file.txt",
		"Write file.txt",
		"sed -i 's/a/b/' file.txt",
	}

	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			if got := Classify(command); got == Implement {
				t.Fatalf("Classify(%q) = Implement, want non-Implement", command)
			}
		})
	}
}
