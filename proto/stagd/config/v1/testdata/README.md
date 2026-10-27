# stagd.yaml as it reads

Each `<name>.yaml` is a stagd.yaml; `<name>.json` is how it reads, as
`stagd.config.v1.Config` in protojson with the proto field names:

    {"config": {...}}         it reads, defaults filled in
    {"problems": ["...", …]}  it doesn't, and these are what to fix, in order

Every reader of stagd.yaml runs them: the platform's server Worker
(`workers/server/test/config-fixtures.test.ts`, from a copy kept in step by
`workers/server/scripts/stagd-yaml-fixtures.ts`). stagd's own tests
(`internal/configschema`) check each `config` is a valid `Config`, and that
stagd.yaml's JSON Schema takes each file that reads. A change to how
stagd.yaml reads starts here.
