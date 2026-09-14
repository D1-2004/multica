CREATE INDEX CONCURRENTLY IF NOT EXISTS product_feature_release_history_idx ON product_feature_release (feature_id, published_at DESC, id DESC);
