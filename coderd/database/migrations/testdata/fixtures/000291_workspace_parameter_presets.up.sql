INSERT INTO public.organizations (id, name, description, created_at, updated_at, is_default, display_name, icon) VALUES ('20362772-802a-4a72-8e4f-3648b4bfd168', 'strange_hopper58', 'wizardly_stonebraker60', '2025-02-07 07:46:19.507551 +00:00', '2025-02-07 07:46:19.507552 +00:00', false, 'competent_rhodes59', '');

INSERT INTO public.users (id, email, username, hashed_password, created_at, updated_at, status, rbac_roles, login_type, avatar_url, deleted, last_seen_at, quiet_hours_schedule, theme_preference, name, github_com_user_id, hashed_one_time_passcode, one_time_passcode_expires_at) VALUES ('6c353aac-20de-467b-bdfb-3c30a37adcd2', 'vigorous_murdock61', 'affectionate_hawking62', 'lqTu9C5363AwD7NVNH6noaGjp91XIuZJ', '2025-02-07 07:46:19.510861 +00:00', '2025-02-07 07:46:19.512949 +00:00', 'active', '{}', 'password', '', false, '0001-01-01 00:00:00.000000', '', '', 'vigilant_hugle63', null, null, null);

INSERT INTO public.templates (id, created_at, updated_at, organization_id, deleted, name, provisioner, active_version_id, description, default_ttl, created_by, icon, user_acl, group_acl, display_name, allow_user_cancel_workspace_jobs, allow_user_autostart, allow_user_autostop, failure_ttl, time_til_dormant, time_til_dormant_autodelete, autostop_requirement_days_of_week, autostop_requirement_weeks, autostart_block_days_of_week, require_active_version, deprecated, activity_bump, max_port_sharing_level) VALUES ('6b298946-7a4f-47ac-9158-b03b08740a41', '2025-02-07 07:46:19.513317 +00:00', '2025-02-07 07:46:19.513317 +00:00', '20362772-802a-4a72-8e4f-3648b4bfd168', false, 'modest_leakey64', 'echo', 'e6cfa2a4-e4cf-4182-9e19-08b975682a28', 'upbeat_wright65', 604800000000000, '6c353aac-20de-467b-bdfb-3c30a37adcd2', 'nervous_keller66', '{}', '{"20362772-802a-4a72-8e4f-3648b4bfd168": ["read", "use"]}', 'determined_aryabhata67', false, true, true, 0, 0, 0, 0, 0, 0, false, '', 3600000000000, 'owner');
INSERT INTO public.template_versions (id, template_id, organization_id, created_at, updated_at, name, readme, job_id, created_by, external_auth_providers, message, archived, source_example_id) VALUES ('af58bd62-428c-4c33-849b-d43a3be07d93', '6b298946-7a4f-47ac-9158-b03b08740a41', '20362772-802a-4a72-8e4f-3648b4bfd168', '2025-02-07 07:46:19.514782 +00:00', '2025-02-07 07:46:19.514782 +00:00', 'distracted_shockley68', 'sleepy_turing69', 'f2e2ea1c-5aa3-4a1d-8778-2e5071efae59', '6c353aac-20de-467b-bdfb-3c30a37adcd2', '[]', '', false, null);

INSERT INTO public.template_version_presets (id, template_version_id, name, created_at) VALUES ('28b42cc0-c4fe-4907-a0fe-e4d20f1e9bfe', 'af58bd62-428c-4c33-849b-d43a3be07d93', 'test', '0001-01-01 00:00:00.000000 +00:00');

-- Add presets with the same template version ID and name
-- to ensure they're correctly handled by the 00031*_preset_prebuilds migration.
INSERT INTO public.template_version_presets (
	id, template_version_id, name, created_at
)
VALUES (
	'c9dd1a63-f0cf-446e-8d6f-2d29d7c8e38b',
	'af58bd62-428c-4c33-849b-d43a3be07d93',
	'duplicate_name',
	'0001-01-01 00:00:00.000000 +00:00'
);

INSERT INTO public.template_version_presets (
	id, template_version_id, name, created_at
)
VALUES (
	'80f93d57-3948-487a-8990-bb011fb80a18',
	'af58bd62-428c-4c33-849b-d43a3be07d93',
	'duplicate_name',
	'0001-01-01 00:00:00.000000 +00:00'
);

INSERT INTO public.template_version_preset_parameters (id, template_version_preset_id, name, value) VALUES ('ea90ccd2-5024-459e-87e4-879afd24de0f', '28b42cc0-c4fe-4907-a0fe-e4d20f1e9bfe', 'test', 'test');
