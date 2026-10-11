DROP TABLE consultation_customer_forms;
DROP TABLE consultation_vet_forms;
DROP TABLE consultation_attachments;
DROP TABLE consultation_messages;
DROP TABLE consultation_chats;
DELETE FROM business_config WHERE kind='chat' AND id='consultation';
