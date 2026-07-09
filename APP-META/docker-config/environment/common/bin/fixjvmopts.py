#!/usr/bin/env python
import sys


class JvmOption:
	def __init__(self, key):
		self.key, self.index, self.opt, self.value = key, -1, None, 0
		self.newOpt, self.newValue = None, 0


def main(os_mem, opts):
	Xmx, Xms, Xmn, XXmn, XXmaxn = [ JvmOption(e) for e in 
		["-Xmx", "-Xms", "-Xmn", "-XX:NewSize=", "-XX:MaxNewSize=",] ]
	index = -1
	for opt in opts:
		index += 1
		for e in [Xmx, Xms, Xmn, XXmn, XXmaxn]:
			if opt.startswith(e.key):
				x = opt[len(e.key):]
				if x.endswith("m"):
					x = int(x[:-len("m")])
				elif x.endswith("g"):
					x = int(x[:-len("g")])
					x *= 1024
				else:
					print >>sys.stderr, "ERROR: invalid jvm option:", opt, "must use 'm' or 'g'"
					sys.exit(1)
				e.index, e.opt, e.value = [index, opt, x,]

	# [Xmn, XXmn,] only keep one, use the later.
	if XXmn.index > Xmn.index:
		Xmn = XXmn

	calc_mem = False
	if Xmx.index < 0:
		print >>sys.stderr, "WARN:", "jvm opt not set:", Xmx.key
		if os_mem > 0:
			calc_mem = True
	elif os_mem > 0 and Xmx.value > os_mem:
		print >>sys.stderr, "WARN:", "jvm opt larger than os memory (%dm):" % os_mem, Xmx.opt
		calc_mem = True

	if calc_mem:
		XmxValue = os_mem < 3072 and int(os_mem / 2) or int(os_mem - 1536 * (os_mem / 5000 + 1))
		XmnValue = int(XmxValue / 2 / 100 * 100)
		for e, v in [ (Xmx, XmxValue), (Xms, XmxValue), (Xmn, XmnValue) ]:
			e.newOpt, e.newValue = "%s%dm" % (e.key, v), v
		# update opts
		appendIndex, insertList = 0, []
		for e in [Xmx, Xms, Xmn]:
			if e.index >= 0:
				# update append index to biggest e.index
				if e.index > appendIndex:
					appendIndex = e.index
				# only fix to smaller
				if e.value > e.newValue:
					print >>sys.stderr, "WARN: fix jvm option:", opts[e.index], "=>", e.newOpt
					opts[e.index] = e.newOpt
			else:
				print >>sys.stderr, "WARN: add jvm option:", e.newOpt
				insertList.append(e)
		insertIndex = appendIndex + 1
		opts[insertIndex:insertIndex] = [e.newOpt for e in insertList]
		# fix insert opt index
		for e in insertList:
			e.index = insertIndex
			insertIndex += 1
		# marks XXmaxn to be deleted
		if XXmaxn.index >= 0:
			print >>sys.stderr, "WARN: delete jvm option:", XXmaxn.opt
			XXmaxn.index = -1

		# delete "-Xm" options not touched by us
		for opt, index in zip(opts, range(len(opts))):
			for e in [Xmx, Xms, Xmn, XXmaxn,]:
				if opt.startswith(e.key) and index != e.index:
					opts[index] = None
		opts = [ e for e in opts if e ]
		# delete cms options if need
		cmsMinValue = 3000
		if Xmx.newValue < cmsMinValue:
			print >>sys.stderr, "INFO: jvm option '%s' less than %dm, delete cms options." % (Xmx.newOpt, cmsMinValue)
			cmsOpts = [ "-XX:+CMSClassUnloadingEnabled", "-XX:CMSInitiatingOccupancyFraction", 
				"-XX:CMSMaxAbortablePrecleanTime", "-XX:+CMSParallelRemarkEnabled", 
				"-XX:+CMSPermGenSweepingEnabled", "-XX:MaxNewSize", "-XX:MaxTenuringThreshold", 
				"-XX:SurvivorRatio", "-XX:+UseCMSCompactAtFullCollection", "-XX:+UseCMSInitiatingOccupancyOnly", 
				"-XX:+UseCompressedOops", "-XX:+UseCompressedOop", "-XX:+UseConcMarkSweepGC", ]
			opts = [ e for e in opts if e and not [k for k in cmsOpts if e.startswith(k)] ]

	print " ".join(opts)


if __name__ == "__main__":
	opts = list(sys.argv) # clone
	opts.pop(0) # app path, e.g. "-"
	os_mem = int(opts.pop(0))
	main(os_mem, opts)