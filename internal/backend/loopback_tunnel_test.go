package backend

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// A LINKED LOOPBACK ADDRESS IS NOT THE STACK `palbase start` BROUGHT UP HERE.
//
// 0.65.0 moved a loopback link out of the committed file into this machine's
// state — the right place, a port on one machine must not be committed. But it
// moved it into the SAME record `palbase start` keeps, and `ReadTarget` marks
// everything read from there as `Local`. A tunnel is loopback too: the control
// plane deploys with `kubectl port-forward` onto 127.0.0.1:18098 and
// `palbase link http://127.0.0.1:18098`, and from 0.65.0 every one of those
// deploys failed with "this checkout is pointed at the stack running on this
// machine" (measured, palbase-cloud cloud-deploy, 2026-09-12 16:19 onward).
// Where an address is STORED and whether a stack was STARTED here are two facts.

func TestAPushThroughALinkedLoopbackTunnelIsNotRefusedAsALocalStack(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, WriteSelfHostTarget(Target{URL: "http://127.0.0.1:1"}))
	target, err := ReadTarget()
	require.NoError(t, err)

	pushErr := runStackPush(context.Background(), target, Credentials{}, false, false, io.Discard)
	require.Error(t, pushErr, "nothing listens on port 1; the push must fail for THAT reason")
	require.NotContains(t, pushErr.Error(), "pointed at the stack running on this machine",
		"a linked tunnel was refused as if `palbase start` had brought it up here")
}

// The refusal itself is right and must survive: a stack started here serves
// the directory and never loads a pushed artifact.
func TestAPushIntoTheStackStartedHereIsStillRefused(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, WriteLocalTarget(Target{URL: "http://127.0.0.1:1"}))
	target, err := ReadTarget()
	require.NoError(t, err)

	pushErr := runStackPush(context.Background(), target, Credentials{}, false, false, io.Discard)
	require.Error(t, pushErr)
	require.Contains(t, pushErr.Error(), "pointed at the stack running on this machine")
}

func TestALinkedLoopbackTunnelResolvesAsOneInstallation(t *testing.T) {
	inScratchCheckout(t)
	resolverRig(t, nil)
	require.NoError(t, WriteSelfHostTarget(Target{URL: "http://127.0.0.1:18098"}))

	got, err := ResolveFor(cmdFor(t))
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:18098", got.URL)
	require.NotEqual(t, "local", got.Source, "a linked tunnel resolved as the stack started here")
	require.False(t, got.Target.Local)
}

func TestALinkedLoopbackTunnelRefusesTheEnvironmentFlagByName(t *testing.T) {
	inScratchCheckout(t)
	resolverRig(t, nil)
	SelectedEnvFlag = "staging"
	require.NoError(t, WriteSelfHostTarget(Target{URL: "http://127.0.0.1:18098"}))

	_, err := ResolveFor(cmdFor(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "one installation")
}
