-- Only reverse the untouched launch settings; never rewrite financial history.
UPDATE referral_settings
SET enabled=false,provider_enabled=false,invitee_bonus_enabled=false,
    invitee_bonus=0,vet_bps=0,grooming_bps=0,sampling_bps=0,
    basis='commission',revision=revision+1
WHERE id=1 AND revision=2 AND enabled AND provider_enabled
  AND invitee_bonus_enabled AND NOT referrer_bonus_enabled
  AND referrer_bonus=0 AND invitee_bonus=200000
  AND vet_bps=1000 AND grooming_bps=1000 AND sampling_bps=1000
  AND basis='platform_share';
