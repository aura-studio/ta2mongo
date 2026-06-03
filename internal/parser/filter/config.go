package filter

// Config configures the expr-lang include/exclude reporting filter. It is the
// filter module's own configuration; the top-level config package references it
// by pointer and the loader unmarshals the filter.* keys into it.
type Config struct {
	// Include is a list of expr-lang expressions. If non-empty, a parsed record
	// is kept only when at least one expression evaluates to true (OR
	// semantics). An empty list lets every record through this stage.
	// Expressions are evaluated against the flattened record document, so fields
	// like "#type", "#event_name", and any "properties.*" keys are accessible at
	// the top level (e.g. `#type == "track"`).
	Include []string `mapstructure:"include"`
	// Exclude is a list of expr-lang expressions. A parsed record is dropped if
	// any expression evaluates to true. Applied after Include.
	Exclude []string `mapstructure:"exclude"`
}

// Build compiles the configured expressions into a *Filter. A nil receiver or
// empty rule set yields a no-op filter that keeps every record.
func (c *Config) Build() (*Filter, error) {
	if c == nil {
		return New(nil, nil)
	}
	return New(c.Include, c.Exclude)
}

// Validate checks that the include/exclude expressions compile. A nil receiver
// is valid (no-op filter).
func (c *Config) Validate() error {
	_, err := c.Build()
	return err
}

// RegisterDefaults registers this module's config keys (under prefix) with the
// given setter so env binding works.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".include", []string{})
	set(prefix+".exclude", []string{})
}
