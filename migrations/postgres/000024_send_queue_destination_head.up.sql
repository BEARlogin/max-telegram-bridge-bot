CREATE INDEX IF NOT EXISTS idx_send_queue_destination_head
ON send_queue(direction, dst_chat_id, id);
