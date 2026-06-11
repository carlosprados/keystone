package recipe

import "testing"

const processRecipe = `
[metadata]
name = "app"
version = "1.0.0"
[lifecycle.run.exec]
command = "./app"
args = ["--port", "8080"]
`

const containerRecipe = `
[metadata]
name = "web"
version = "2.1.0"
[lifecycle.run]
type = "container"
restart_policy = "always"
[lifecycle.run.container]
image = "docker.io/library/nginx:alpine"
`

func TestUnmarshalAcceptsValid(t *testing.T) {
	if _, err := Unmarshal([]byte(processRecipe)); err != nil {
		t.Errorf("process recipe rejected: %v", err)
	}
	if _, err := Unmarshal([]byte(containerRecipe)); err != nil {
		t.Errorf("container recipe rejected: %v", err)
	}
}

func TestUnmarshalRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"missing lifecycle": `
[metadata]
name = "x"
version = "1.0.0"
`,
		"exec without command": `
[metadata]
name = "x"
version = "1.0.0"
[lifecycle.run.exec]
args = ["a"]
`,
		"bad restart_policy": `
[metadata]
name = "x"
version = "1.0.0"
[lifecycle.run]
restart_policy = "whenever"
[lifecycle.run.exec]
command = "./x"
`,
		"traversal name": `
[metadata]
name = "../../escape"
version = "1.0.0"
[lifecycle.run.exec]
command = "./x"
`,
	}
	for name, toml := range cases {
		if _, err := Unmarshal([]byte(toml)); err == nil {
			t.Errorf("%s: Unmarshal accepted an invalid recipe, want error", name)
		}
	}
}
