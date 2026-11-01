package rules

import (
	"testing"
	"time"
)

// provider_health must expose a FIXED provider by name even when the request's
// own provider context is empty — the drain-primary use case.
func TestProviderHealthTableFixedProvider(t *testing.T) {
	now := time.Now()
	table := map[string]map[string]KeyHealthSnapshot{
		"LLM Gateway (DevPass)": {
			"809e4a23": {
				ConsecutiveFailures: 3,
				LastFailureClass:    "quota",
				LastStatusCode:      402,
				LastErrorMessage:    "daily pacing allowance exceeded",
				LastFailureAt:       &now,
			},
		},
		"LLM Gateway": {
			"": {LastSuccessAt: now, ConsecutiveFailures: 0},
		},
	}
	vars := map[string]any{
		"provider":        "",
		"provider_health": providerHealthTable(table),
	}

	env, err := createCELEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	ast, iss := env.Compile(`provider_health["LLM Gateway (DevPass)"]["*"].healthy == false`)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("compile: %v", iss.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := prg.Eval(vars)
	if err != nil {
		t.Fatal(err)
	}
	if out.Value() != true {
		t.Fatalf("want true, got %v", out)
	}

	// last_status_code by name, and a never-failed provider reads healthy.
	ast2, iss2 := env.Compile(`provider_health["LLM Gateway (DevPass)"]["*"].last_status_code == 402`)
	if iss2 != nil && iss2.Err() != nil {
		t.Fatalf("compile2: %v", iss2.Err())
	}
	prg2, _ := env.Program(ast2)
	out2, _, _ := prg2.Eval(vars)
	if out2.Value() != true {
		t.Fatalf("status check: want true, got %v", out2)
	}
	ast3, iss3 := env.Compile(`provider_health["LLM Gateway"]["*"].healthy == true`)
	if iss3 != nil && iss3.Err() != nil {
		t.Fatalf("compile3: %v", iss3.Err())
	}
	prg3, _ := env.Program(ast3)
	out3, _, _ := prg3.Eval(vars)
	if out3.Value() != true {
		t.Fatalf("healthy fallback provider: want true, got %v", out3)
	}
}

// End-to-end guard for the production rule text shipped in the devpass_down row.
func TestProductionRuleExpressionCompilesAndEvaluates(t *testing.T) {
	env, err := createCELEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	expr := `model == "glm-5.3-flash" && "LLM Gateway (DevPass)" in provider_health && provider_health["LLM Gateway (DevPass)"]["*"].healthy == false`
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("compile: %v", iss.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		t.Fatal(err)
	}

	notripped := map[string]any{"model": "glm-5.3-flash", "provider_health": map[string]any{}}
	out, _, err := prg.Eval(notripped)
	if err != nil {
		t.Fatalf("empty table eval should not error: %v", err)
	}
	if out.Value() != false {
		t.Fatalf("empty table should not trip, got %v", out)
	}

	now := time.Now()
	tripped := map[string]any{"model": "glm-5.3-flash", "provider_health": providerHealthTable(map[string]map[string]KeyHealthSnapshot{
		"LLM Gateway (DevPass)": {"809e4a23": {ConsecutiveFailures: 2, LastFailureClass: "quota", LastStatusCode: 402, LastFailureAt: &now}},
	})}
	out2, _, err := prg.Eval(tripped)
	if err != nil {
		t.Fatalf("tripped eval: %v", err)
	}
	if out2.Value() != true {
		t.Fatalf("unhealthy DevPass should trip, got %v", out2)
	}
}
