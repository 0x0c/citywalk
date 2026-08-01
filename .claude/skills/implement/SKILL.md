---
name: implement
model: opus
description: Implement a citywalk roadmap item end to end — resolve, plan, code, self-review, and ship — inside the guardrails in .agent-workflows/implement/workflow.md. Use when asked to implement, build, or ship a CW-NNNN roadmap item.
---

# Claude adapter

Read `.agent-workflows/implement/workflow.md` completely, then follow it exactly, in order. The
Guardrails section binds every step below it; none of them bend for convenience. Do not write
implementation code before the plan in step 5 is approved, and do not present the change as done
while step 7 (the mechanical gate) or step 8 (the semantic self-review) still has an open finding —
keep reviewing and fixing until one full pass of both turns up nothing.
