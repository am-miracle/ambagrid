INSERT INTO grid_operators (operator_id, name)
VALUES ('operator-0101', 'Demo Operator')
ON CONFLICT (operator_id) DO NOTHING;

INSERT INTO sites (site_id, name, country, region, operator_id)
SELECT s, initcap(replace(s, '-', ' ')), 'NG', initcap(split_part(s, '-', 1)), 'operator-0101'
FROM unnest(ARRAY[
  'rivers-bolo', 'lagos-epe', 'imo-ohaji', 'nasarawa-duduguru', 'bayelsa-oweikorogha',
  'akwa-ibom-ibeno', 'bauchi-darazo', 'benue-otukpo', 'borno-monguno', 'cross-river-ikom',
  'delta-burutu', 'ebonyi-abakaliki', 'enugu-nsukka', 'kaduna-kachia', 'kano-bagwai',
  'kwara-patigi', 'niger-wushishi', 'sokoto-illela', 'taraba-ibi', 'yobe-geidam'
]) AS s
ON CONFLICT (site_id) DO NOTHING;
