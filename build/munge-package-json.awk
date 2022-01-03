/"pulumi"/ {
	print;
	print "        \"name\": \"docker-buildkit\",";
	next;
}

{
	gsub("\\${VERSION}", version);
	gsub("pluginDownloadURL", "server");
	print;
}
