package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStatePlan lays out a plan whose components declare the given state
// versions. A nil version means the recipe omits the block entirely, which is
// a different answer from zero.
func writeStatePlan(t *testing.T, dir, planName string, versions map[string]*int) string {
	t.Helper()

	var plan strings.Builder
	for name, v := range versions {
		recipePath := filepath.Join(dir, planName+"-"+name+".recipe.toml")

		var rec strings.Builder
		rec.WriteString("[metadata]\n")
		rec.WriteString("name = \"com.example." + name + "\"\n")
		rec.WriteString("version = \"1.0.0\"\n\n")
		rec.WriteString("[lifecycle.run]\ntype = \"process\"\n\n")
		rec.WriteString("[lifecycle.run.exec]\ncommand = \"/bin/true\"\n")
		if v != nil {
			rec.WriteString("\n[lifecycle.run.state]\nversion = " + itoa(*v) + "\n")
		}
		if err := os.WriteFile(recipePath, []byte(rec.String()), 0o644); err != nil {
			t.Fatalf("write recipe: %v", err)
		}

		plan.WriteString("[[components]]\n")
		plan.WriteString("name = \"" + name + "\"\n")
		plan.WriteString("recipe = \"" + recipePath + "\"\n\n")
	}

	planPath := filepath.Join(dir, planName+".toml")
	if err := os.WriteFile(planPath, []byte(plan.String()), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return planPath
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func intp(i int) *int { return &i }

// TestStateMigrationBlocksBackwardRollback is the case the field exists for:
// the new build migrated the data forward, so putting the old binary back
// leaves it facing state it cannot read.
func TestStateMigrationBlocksBackwardRollback(t *testing.T) {
	dir := t.TempDir()
	a := New(Options{InsecureSkipVerify: true})

	oldPath := writeStatePlan(t, dir, "old", map[string]*int{"api": intp(20)})
	newPath := writeStatePlan(t, dir, "new", map[string]*int{"api": intp(21)})

	desired, err := a.loadPlannedState(newPath)
	if err != nil {
		t.Fatalf("load desired: %v", err)
	}

	blockers, _ := a.stateMigrationBlockers(oldPath, desired)
	if len(blockers) != 1 || !strings.Contains(blockers[0], "21 -> 20") {
		t.Fatalf("expected the rollback to be blocked naming both versions, got %v", blockers)
	}
}

// TestStateMigrationAllowsSameVersion: reverting a binary within one state
// version is the normal case and must stay unobstructed.
func TestStateMigrationAllowsSameVersion(t *testing.T) {
	dir := t.TempDir()
	a := New(Options{InsecureSkipVerify: true})

	oldPath := writeStatePlan(t, dir, "old", map[string]*int{"api": intp(20)})
	newPath := writeStatePlan(t, dir, "new", map[string]*int{"api": intp(20)})

	desired, _ := a.loadPlannedState(newPath)
	blockers, unchecked := a.stateMigrationBlockers(oldPath, desired)
	if len(blockers) != 0 || len(unchecked) != 0 {
		t.Fatalf("expected a clean rollback, got blockers=%v unchecked=%v", blockers, unchecked)
	}
}

// TestStateMigrationAllowsRollingForward: the previous plan being AHEAD is not
// a migration crossing — nothing was migrated by the apply that just failed.
func TestStateMigrationAllowsRollingForward(t *testing.T) {
	dir := t.TempDir()
	a := New(Options{InsecureSkipVerify: true})

	oldPath := writeStatePlan(t, dir, "old", map[string]*int{"api": intp(21)})
	newPath := writeStatePlan(t, dir, "new", map[string]*int{"api": intp(20)})

	desired, _ := a.loadPlannedState(newPath)
	if blockers, _ := a.stateMigrationBlockers(oldPath, desired); len(blockers) != 0 {
		t.Fatalf("expected no blockers, got %v", blockers)
	}
}

// TestStateMigrationZeroIsAVersion guards the distinction the pointer exists
// for: state before the first migration is version 0, and 0 -> absent must not
// be read as equal.
func TestStateMigrationZeroIsAVersion(t *testing.T) {
	dir := t.TempDir()
	a := New(Options{InsecureSkipVerify: true})

	oldPath := writeStatePlan(t, dir, "old", map[string]*int{"api": intp(0)})
	newPath := writeStatePlan(t, dir, "new", map[string]*int{"api": intp(1)})

	desired, _ := a.loadPlannedState(newPath)
	blockers, _ := a.stateMigrationBlockers(oldPath, desired)
	if len(blockers) != 1 || !strings.Contains(blockers[0], "1 -> 0") {
		t.Fatalf("expected 1 -> 0 to block, got %v", blockers)
	}
}

// TestStateMigrationUnknownIsReportedNotEnforced: a version on only one side
// cannot be compared. Refusing there would make the field impossible to adopt,
// because the first plan to declare one always faces a predecessor that does
// not.
func TestStateMigrationUnknownIsReportedNotEnforced(t *testing.T) {
	dir := t.TempDir()
	a := New(Options{InsecureSkipVerify: true})

	oldPath := writeStatePlan(t, dir, "old", map[string]*int{"api": nil})
	newPath := writeStatePlan(t, dir, "new", map[string]*int{"api": intp(21)})

	desired, _ := a.loadPlannedState(newPath)
	blockers, unchecked := a.stateMigrationBlockers(oldPath, desired)
	if len(blockers) != 0 {
		t.Fatalf("expected no blockers, got %v", blockers)
	}
	if len(unchecked) != 1 || !strings.Contains(unchecked[0], "api") {
		t.Fatalf("expected the component to be reported as uncheckable, got %v", unchecked)
	}
}

// TestStateMigrationIgnoresComponentsNotInBothPlans: a component being added
// or removed says nothing about a migration of one that stays.
func TestStateMigrationIgnoresComponentsNotInBothPlans(t *testing.T) {
	dir := t.TempDir()
	a := New(Options{InsecureSkipVerify: true})

	oldPath := writeStatePlan(t, dir, "old", map[string]*int{"api": intp(20)})
	newPath := writeStatePlan(t, dir, "new", map[string]*int{"api": intp(20), "worker": intp(99)})

	desired, _ := a.loadPlannedState(newPath)
	blockers, unchecked := a.stateMigrationBlockers(oldPath, desired)
	if len(blockers) != 0 || len(unchecked) != 0 {
		t.Fatalf("expected the new component to be ignored, got blockers=%v unchecked=%v", blockers, unchecked)
	}
}
