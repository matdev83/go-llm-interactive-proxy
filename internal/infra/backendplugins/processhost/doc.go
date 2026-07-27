// Package processhost owns the project-selected supervised backend-plugin
// process host: lazy activation, process-model ownership, peer-gated local
// channels, generation invalidation, and composition BuildResult cleanup.
//
// Platform honesty: production Windows/Darwin launch and channel factories fail
// closed until an approved exact-identity profile is runtime-proven. Linux
// provides source/compile descriptor-launch and private UDS peercred paths.
package processhost
