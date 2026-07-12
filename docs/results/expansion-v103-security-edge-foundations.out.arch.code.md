I understand the **Code Implementer** role. I've loaded the project context:

- **AGENTS.md** — Engineering constraints, architecture, feature matrix
- **BOOTSTRAP.md** — Project identity, tech stack, key conventions
- **CURRENT_SPRINT.md** — Sprint completed: integration validation ✅
- **TASK.md** — Previous task completed: context chain setup ✅

I'm ready to implement new features according to architecture design documents (`.arch.md`) and implementation plans (`.impl-plan.md`).

**To proceed, I need:**
1. The specific `.arch.md` file(s) detailing the architecture of what to implement
2. The corresponding `.impl-plan.md` file(s) with the implementation plan
3. Any additional requirements or context

I see several `.arch.md` and `.impl-plan.md` files under `docs/results/`. Please point me to which one(s) I should implement, or provide the architecture/plan documents directly.

Key constraints I'll adhere to from `AGENTS.md`:
- ≤500 lines per file, ≤50 lines per function, cyclomatic ≤10
- No `utils/` `common/` `helper/` packages
- I1: SQL placeholder numbering (`$N` → `rebind`)
- I2: Dual migration files for schema changes
- I4: Fixed middleware chain order
- I5: Opt-in safe defaults (AI/etc. off unless configured)
- Run `make check` before submitting
