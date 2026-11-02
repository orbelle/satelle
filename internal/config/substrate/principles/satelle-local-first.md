---
name: satelle-local-first
type: principle
tags: [type:principle, principles:seat]
applies_to: ["*"]
description: The local factory, with no sign-in, is the default and must always work; every third-party service enters through a provider seam chosen by configuration, never baked in, and a missing provider degrades to the local path with a warning.
---

The local factory is the default: every route phase is runnable locally with no
sign-in, and when it works the team and remote structures work too, because
remote is a placement choice, never a requirement. Beyond the local path, a
third-party integration enters through a named provider seam selected by
configuration: the secret resolver by ref scheme, the credential provider by
`from=`, the forge for PR/merge/release, and the agent CLI by profile. The first
provider behind a seam may be concrete ([[satelle-start-small]]), but it is
never referenced outside its seam. Prefer configuration over code and a plugin
over a bake-in; a missing provider degrades to the local path with a warning,
never a block ([[satelle-configure-freely]]).
