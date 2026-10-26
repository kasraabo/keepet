-- Apply the agreed launch terms only to the untouched initial configuration.
-- Existing administrator choices and paid-order snapshots are preserved.
UPDATE referral_settings
SET enabled=true,provider_enabled=true,invitee_bonus_enabled=true,
    invitee_bonus=200000,vet_bps=1000,grooming_bps=1000,sampling_bps=1000,
    basis='platform_share',revision=revision+1
WHERE id=1 AND revision=1 AND NOT enabled AND NOT provider_enabled
  AND NOT invitee_bonus_enabled AND NOT referrer_bonus_enabled
  AND referrer_bonus=0 AND invitee_bonus=0
  AND vet_bps=0 AND grooming_bps=0 AND sampling_bps=0 AND basis='commission';
