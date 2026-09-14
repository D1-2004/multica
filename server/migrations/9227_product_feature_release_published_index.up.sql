CREATE INDEX CONCURRENTLY IF NOT EXISTS product_feature_release_published_idx ON product_feature_release (published_at DESC, id DESC);
