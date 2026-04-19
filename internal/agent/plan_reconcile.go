package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	"github.com/carlosprados/keystone/internal/deploy"
	"github.com/carlosprados/keystone/internal/recipe"
	"github.com/carlosprados/keystone/internal/state"
)

type plannedComponent struct {
	item         deploy.Component
	rec          *recipe.Recipe
	recipeDigest string
	recipeID     string
	deps         []string
}

type plannedState struct {
	path      string
	plan      *deploy.Plan
	byName    map[string]*plannedComponent
	planMap   []state.PlanComponent
	edges     map[string][]string // dependency -> dependent
	indegrees map[string]int
}

type reconcileActions struct {
	stopTargets  map[string]bool
	startTargets map[string]bool
	noTouch      map[string]bool
	stopOrder    []string
	startOrder   []string
}

func (a *Agent) ApplyPlan(planPath string, dry bool) error {
	if !a.applyInProgress.CompareAndSwap(false, true) {
		return fmt.Errorf("apply already in progress")
	}
	defer a.applyInProgress.Store(false)
	return a.applyPlanReconcileUnlocked(planPath, dry, true)
}

func (a *Agent) applyPlanReconcileUnlocked(planPath string, dry, allowRollback bool) error {
	desired, err := a.loadPlannedState(planPath)
	if err != nil {
		return err
	}

	a.mu.RLock()
	oldPlanPath := a.planPath
	oldPlanComps := append([]state.PlanComponent(nil), a.planComps...)
	a.mu.RUnlock()

	actions, err := a.buildReconcileActions(oldPlanComps, desired)
	if err != nil {
		return err
	}
	log.Printf("[agent] reconcile stop_order=%v start_order=%v no_touch=%v", actions.stopOrder, actions.startOrder, sortedKeys(actions.noTouch))

	if dry {
		a.mu.Lock()
		a.planPath = planPath
		a.planStatus = "dry-run"
		a.planErr = ""
		a.planComps = desired.planMap
		a.mu.Unlock()
		a.persistSnapshot()
		log.Printf("[agent] dry-run stop=%v start=%v no_touch=%v", sortedKeys(actions.stopTargets), sortedKeys(actions.startTargets), sortedKeys(actions.noTouch))
		return nil
	}

	a.mu.Lock()
	a.planPath = planPath
	a.planStatus = "applying"
	a.planErr = ""
	a.mu.Unlock()
	a.persistSnapshot()

	for _, name := range actions.stopOrder {
		a.stopComponent(name)
	}

	a.mu.Lock()
	a.applySkipStart = cloneBoolMap(actions.noTouch)
	a.mu.Unlock()

	err = a.applyPlan(planPath)

	a.mu.Lock()
	a.applySkipStart = make(map[string]bool)
	a.mu.Unlock()

	if err == nil {
		return nil
	}

	if !allowRollback || oldPlanPath == "" {
		return err
	}

	log.Printf("[agent] apply failed, attempting rollback to previous plan: %s", oldPlanPath)
	_ = a.stopPlanInternal(false)
	// Restore previous mapping immediately for API introspection while rollback runs.
	a.mu.Lock()
	a.planPath = oldPlanPath
	a.planComps = oldPlanComps
	a.mu.Unlock()
	a.persistSnapshot()

	rbErr := a.applyPlanReconcileUnlocked(oldPlanPath, false, false)
	if rbErr != nil {
		return fmt.Errorf("apply failed: %v; rollback failed: %w", err, rbErr)
	}
	return fmt.Errorf("apply failed and rollback was completed: %w", err)
}

func (a *Agent) loadPlannedState(planPath string) (*plannedState, error) {
	p, err := deploy.Load(planPath)
	if err != nil {
		return nil, err
	}

	seenCompNames := map[string]struct{}{}
	metaToCompName := map[string]string{}
	byName := make(map[string]*plannedComponent, len(p.Components))

	for _, it := range p.Components {
		if _, dup := seenCompNames[it.Name]; dup {
			return nil, fmt.Errorf("plan contains duplicate component name: %s", it.Name)
		}
		seenCompNames[it.Name] = struct{}{}

		r, _, digest, err := a.resolveRecipeRef(it.RecipePath)
		if err != nil {
			return nil, fmt.Errorf("failed to load recipe for component %s (%s): %w", it.Name, it.RecipePath, err)
		}
		if prev, ok := metaToCompName[r.Metadata.Name]; ok {
			return nil, fmt.Errorf("duplicate recipe metadata.name %q in plan (components %s and %s)", r.Metadata.Name, prev, it.Name)
		}
		metaToCompName[r.Metadata.Name] = it.Name
		byName[it.Name] = &plannedComponent{
			item:         it,
			rec:          r,
			recipeDigest: digest,
			recipeID:     recipeIdentity(r),
		}
	}

	edges := map[string][]string{}
	indeg := map[string]int{}
	for name := range byName {
		indeg[name] = 0
	}

	for name, comp := range byName {
		for _, dep := range comp.rec.Dependencies {
			depCompName, ok := metaToCompName[dep.Name]
			if !ok {
				if isHardDependency(dep.Type) {
					return nil, fmt.Errorf("component %s has hard dependency %q not present in plan", name, dep.Name)
				}
				log.Printf("[agent] component=%s msg=soft dependency %q not present in plan (ignored)", name, dep.Name)
				continue
			}
			depComp := byName[depCompName]
			satisfied, derr := dependencyVersionSatisfied(dep.Version, depComp.rec.Metadata.Version)
			if derr != nil {
				return nil, fmt.Errorf("component %s dependency %q has invalid version constraint %q: %w", name, dep.Name, dep.Version, derr)
			}
			if !satisfied {
				if isHardDependency(dep.Type) {
					return nil, fmt.Errorf("component %s hard dependency %q constraint %q not satisfied by %s", name, dep.Name, dep.Version, depComp.rec.Metadata.Version)
				}
				log.Printf("[agent] component=%s msg=soft dependency %q constraint %q not satisfied by %s (ignored)", name, dep.Name, dep.Version, depComp.rec.Metadata.Version)
				continue
			}
			comp.deps = append(comp.deps, depCompName)
			edges[depCompName] = append(edges[depCompName], name)
			indeg[name]++
		}
	}

	order := topoOrder(edges, indeg)
	if len(order) != len(byName) {
		return nil, fmt.Errorf("dependency graph has a cycle")
	}

	planMap := make([]state.PlanComponent, 0, len(p.Components))
	for _, it := range p.Components {
		comp := byName[it.Name]
		deps := append([]string(nil), comp.deps...)
		sort.Strings(deps)
		planMap = append(planMap, state.PlanComponent{
			Name:          it.Name,
			RecipePath:    it.RecipePath,
			RecipeMeta:    comp.rec.Metadata.Name,
			RecipeVersion: comp.rec.Metadata.Version,
			RecipeID:      comp.recipeID,
			RecipeDigest:  comp.recipeDigest,
			Deps:          deps,
		})
	}

	return &plannedState{
		path:      planPath,
		plan:      p,
		byName:    byName,
		planMap:   planMap,
		edges:     edges,
		indegrees: indeg,
	}, nil
}

func (a *Agent) buildReconcileActions(oldPlan []state.PlanComponent, desired *plannedState) (*reconcileActions, error) {
	oldByName := make(map[string]state.PlanComponent, len(oldPlan))
	oldEdges := map[string][]string{}
	for _, pc := range oldPlan {
		oldByName[pc.Name] = pc
		for _, d := range pc.Deps {
			oldEdges[d] = append(oldEdges[d], pc.Name)
		}
	}

	stopTargets := map[string]bool{}
	startTargets := map[string]bool{}
	noTouch := map[string]bool{}

	changedRoots := map[string]bool{}
	for name, comp := range desired.byName {
		old, exists := oldByName[name]
		if !exists {
			startTargets[name] = true
			changedRoots[name] = true
			continue
		}
		if a.componentChanged(old, comp) {
			stopTargets[name] = true
			startTargets[name] = true
			changedRoots[name] = true
			continue
		}
		noTouch[name] = true
	}
	for name := range oldByName {
		if _, ok := desired.byName[name]; !ok {
			stopTargets[name] = true
		}
	}

	queue := make([]string, 0, len(changedRoots))
	for n := range changedRoots {
		queue = append(queue, n)
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		for _, dep := range desired.edges[cur] {
			if _, existsOld := oldByName[dep]; !existsOld {
				continue
			}
			if startTargets[dep] {
				continue
			}
			startTargets[dep] = true
			stopTargets[dep] = true
			queue = append(queue, dep)
		}
	}

	// Components left untouched must currently satisfy readiness.
	for name := range noTouch {
		comp := desired.byName[name]
		requireHealth := strings.TrimSpace(comp.rec.Lifecycle.Run.Health.Check) != ""
		if a.componentIsReady(name, requireHealth) {
			continue
		}
		startTargets[name] = true
		stopTargets[name] = true
		queue = append(queue, name)
	}
	// Propagate restarts to dependents from newly forced restarts above.
	seen = map[string]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		for _, dep := range desired.edges[cur] {
			if _, existsOld := oldByName[dep]; !existsOld {
				continue
			}
			if startTargets[dep] {
				continue
			}
			startTargets[dep] = true
			stopTargets[dep] = true
			queue = append(queue, dep)
		}
	}

	noTouch = map[string]bool{}
	for name := range desired.byName {
		if !startTargets[name] {
			noTouch[name] = true
		}
	}

	stopOrder, err := inducedTopoOrder(stopTargets, oldEdges, true)
	if err != nil {
		return nil, err
	}
	startOrder, err := inducedTopoOrder(startTargets, desired.edges, false)
	if err != nil {
		return nil, err
	}

	return &reconcileActions{
		stopTargets:  stopTargets,
		startTargets: startTargets,
		noTouch:      noTouch,
		stopOrder:    stopOrder,
		startOrder:   startOrder,
	}, nil
}

func inducedTopoOrder(nodes map[string]bool, edges map[string][]string, reverse bool) ([]string, error) {
	in := map[string]int{}
	for n := range nodes {
		in[n] = 0
	}
	subEdges := map[string][]string{}
	for from, tos := range edges {
		if !nodes[from] {
			continue
		}
		for _, to := range tos {
			if !nodes[to] {
				continue
			}
			subEdges[from] = append(subEdges[from], to)
			in[to]++
		}
	}

	order := topoOrder(subEdges, in)
	if len(order) != len(in) {
		return nil, fmt.Errorf("cycle detected while ordering reconcile actions")
	}
	if reverse {
		for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
	}
	return order, nil
}

func (a *Agent) componentChanged(old state.PlanComponent, desired *plannedComponent) bool {
	oldID := old.RecipeID
	if strings.TrimSpace(oldID) == "" {
		if old.RecipeMeta != "" && old.RecipeVersion != "" {
			oldID = fmt.Sprintf("%s:%s", old.RecipeMeta, old.RecipeVersion)
		}
	}
	if strings.TrimSpace(oldID) == "" && strings.Contains(old.RecipePath, ":") {
		oldID = old.RecipePath
	}
	if strings.TrimSpace(oldID) == "" {
		if ci, ok := a.comps.Get(old.Name); ok && old.RecipeMeta != "" && ci.Version != "" {
			oldID = fmt.Sprintf("%s:%s", old.RecipeMeta, ci.Version)
		}
	}
	if strings.TrimSpace(oldID) == "" {
		oldID = old.RecipePath
	}
	if oldID != desired.recipeID {
		return true
	}

	oldDigest := strings.TrimSpace(old.RecipeDigest)
	newDigest := strings.TrimSpace(desired.recipeDigest)
	if oldDigest == "" {
		// Conservative choice: if we don't know previous digest, force one restart.
		return true
	}
	if newDigest == "" {
		return false
	}
	return oldDigest != newDigest
}

func (a *Agent) componentIsReady(name string, requireHealth bool) bool {
	ci, ok := a.comps.Get(name)
	if !ok {
		return false
	}
	if ci.State != "running" {
		return false
	}
	if requireHealth {
		return ci.LastHealth == "healthy"
	}
	return true
}

func recipeIdentity(r *recipe.Recipe) string {
	return fmt.Sprintf("%s:%s", r.Metadata.Name, r.Metadata.Version)
}

func (a *Agent) resolveRecipeRef(recipeRef string) (*recipe.Recipe, string, string, error) {
	resolvedPath := recipeRef
	r, err := recipe.Load(recipeRef)
	if err != nil {
		name, version := parseRecipeStoreRef(recipeRef)
		path, serr := a.recipes.GetPath(name, version)
		if serr != nil {
			return nil, "", "", err
		}
		resolvedPath = path
		r, err = recipe.Load(path)
		if err != nil {
			return nil, "", "", err
		}
	}
	b, rerr := os.ReadFile(resolvedPath)
	if rerr != nil {
		// Fallback to deterministic digest from identity when raw source is not directly readable.
		sum := sha256.Sum256([]byte(recipeIdentity(r)))
		return r, resolvedPath, hex.EncodeToString(sum[:]), nil
	}
	sum := sha256.Sum256(b)
	return r, resolvedPath, hex.EncodeToString(sum[:]), nil
}

func parseRecipeStoreRef(ref string) (name, version string) {
	name = ref
	version = ""
	if parts := strings.Split(ref, ":"); len(parts) == 2 {
		return parts[0], parts[1]
	}
	return name, version
}

func isHardDependency(depType string) bool {
	t := strings.ToLower(strings.TrimSpace(depType))
	return t == "" || t == "hard"
}

func dependencyVersionSatisfied(constraint, actual string) (bool, error) {
	if strings.TrimSpace(constraint) == "" {
		return true, nil
	}
	c, err := semver.NewConstraint(strings.TrimSpace(constraint))
	if err != nil {
		return false, err
	}
	v, err := semver.NewVersion(strings.TrimSpace(actual))
	if err != nil {
		return false, err
	}
	return c.Check(v), nil
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
