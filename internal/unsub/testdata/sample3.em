From: Signed Sender <secure@example.com>
To: carol@example.com
Subject: Quarterly report (signed)
Message-ID: <signed-1@example.com>
Date: Fri, 02 Oct 2026 12:00:00 +0000
MIME-Version: 1.0
Content-Type: multipart/signed; protocol="application/pgp-signature"; micalg=pgp-sha256; boundary="sig1"

--sig1
Content-Type: text/plain; charset=us-ascii
Content-Transfer-Encoding: 7bit

Attached is the signed quarterly summary.
--sig1
Content-Type: application/pgp-signature; name="signature.asc"
Content-Description: OpenPGP digital signature
Content-Disposition: attachment; filename="signature.asc"

-----BEGIN PGP SIGNATURE-----
iQEcBAABCgAGBQJfakedummyAAoJEL0rVadDummysign
=dUmMy
-----END PGP SIGNATURE-----
--sig1--
