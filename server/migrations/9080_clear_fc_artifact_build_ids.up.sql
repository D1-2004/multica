UPDATE fc_e2b_stable_release
SET artifact_build_id = '',
    previous_artifact_build_id = ''
WHERE sandbox_backend = 'aliyun_fc';

UPDATE fc_e2b_stable_channel
SET current_artifact_build_id = ''
WHERE sandbox_backend = 'aliyun_fc';

UPDATE fc_e2b_stable_release_target
SET previous_artifact_build_id = ''
WHERE sandbox_backend = 'aliyun_fc';
