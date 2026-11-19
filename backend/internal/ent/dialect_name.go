package ent

// DialectName lets authored schema-domain queries use the actual driver's quoting rules.
func (c *Client) DialectName() string { return c.driver.Dialect() }
