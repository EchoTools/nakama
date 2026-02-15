# GOPHER-LUA PACKAGE

**Scope:** `/internal/gopher-lua` (34 Go files - embedded Lua 5.1 VM)

## OVERVIEW

Embedded Lua 5.1 VM in pure Go. Powers Nakama runtime scripting.

## WHERE TO LOOK

| Task | Files |
|------|-------|
| **VM core** | `state.go`, `vm.go` |
| **Std libs** | `*lib.go` (base, table, string, etc.) |
| **AST** | `ast/*.go` |
| **Parser** | `parse/*.go` |
| **API** | `state.go`, `value.go`, `table.go` |

## CONVENTIONS

- **Lua 5.1 compat**: Not 5.2+
- **Pure Go**: No CGO
- **Value interface**: `LValue` for all Lua values
- **State-based**: `LState` manages VM

## PATTERNS

```go
// VM usage
L := lua.NewState()
defer L.Close()
L.DoString(`print("Hello")`)

// Call Lua from Go
L.CallByParam(lua.P{
    Fn: L.GetGlobal("myfunc"),
    NRet: 1,
    Protect: true,
}, lua.LNumber(42))

// Register Go func in Lua
L.SetGlobal("mygofunction", L.NewFunction(func(L *lua.LState) int {
    arg := L.CheckString(1)
    L.Push(lua.LString("result"))
    return 1 // return count
}))

// Lua table from Go
tbl := L.NewTable()
tbl.RawSetString("key", lua.LString("value"))
L.Push(tbl)
```

## NOTES

- **Vendored**: Not a module dep
- **Nakama runtime**: Powers `server/runtime_lua_*.go`
- **Production-tested**: Years in prod
- **Performance**: ~10-20x slower than LuaJIT but pure Go
- **Full stdlib**: table, string, math, io, os, coroutine
- **Custom extensions**: Nakama adds via `runtime_lua_nakama.go`
- **Pattern matching**: `pm/` package (not regex)
- **Coroutines**: Full support via `lua.LThread`
- **Test suite**: `_lua5.1-tests/` runs official tests
- **Stable**: Rarely needs changes
