package configcode

// TODO(Faz1-C): notifications serializer.
//
// Implement a ModuleSerializer that pulls the project's notification
// provider config and writes config/notifications.toml. Mirror flags.go:
//
//  1. Uncomment the init() below to register it.
//  2. Add Name() -> "notifications", Filename() -> "notifications.toml".
//  3. Add Pull(ctx, ref, sc) that queries the notifications config tRPC
//     procedure and serializes to TOML via structs.
//
// SECRETS: notification providers carry API keys. NEVER serialize the
// value — emit SecretRef("<NAME>") (i.e. "@secret/<NAME>"). Public
// fields (provider name, from-address) serialize plainly.
//
// NOTE (plan Faz 4): notifications has no Studio tRPC config router yet
// (module HTTP only). If the GET procedure is missing when this is
// picked up, return a header-only document (empty config) rather than
// failing the whole pull — mirror flags.go's empty-project behaviour.
//
// func init() { Register(notificationsSerializer{}) }
//
// type notificationsSerializer struct{}
